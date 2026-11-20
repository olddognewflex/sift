package audit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/olddognewflex/sift/internal/agent"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/cost"
	"github.com/olddognewflex/sift/internal/findings"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/source"
	"github.com/olddognewflex/sift/internal/storage"
)

const taskVerification = "verification"

// identicalReason is the outcome reason when the verified snapshot is the
// audited one, so no agent runs (ADR 0006 §2).
const identicalReason = "snapshot identical to the audited one; no agent ran"

// verificationSection is the heading Pruv's instructions must have for
// verify tasks (agents/pruv.md, version 3).
const verificationSection = "## Verification tasks"

// VerifyOptions configures one verify run (ADR 0006). Options.RepoPath
// defaults to the audited run's repository; Options.Preflight approves an
// over-budget cost estimate exactly as it does for an audit.
type VerifyOptions struct {
	Options
	// RunID is the audit run to verify; empty means the latest audit run.
	RunID string
	// Findings narrows the selection; empty means every confirmed and
	// supported_risk finding.
	Findings []string
}

// verifier holds a verify run's inputs and the records decided so far.
type verifier struct {
	target contract.VerifyTarget
	orig   *storage.Run
	// items are the selected findings as reported for the audited run, in
	// target.Findings order, with their effective ratings and decisions.
	items     []report.Item
	discovery *contract.DiscoveryReport
	log       *recordLog[contract.VerificationRecord]
}

// Verify re-checks established findings of an audit run in a new run
// linked to it. Every refusal (ADR 0006 §8) returns an error before a run
// is created. Once the run exists, Verify returns it together with any
// error; the manifest status says whether verification completed. The
// audited run is only read.
func Verify(ctx context.Context, vopts VerifyOptions) (*Result, error) {
	opts := vopts.Options
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cfg := opts.Config
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	v, om, err := loadOriginal(opts.Store, vopts.RunID, vopts.Findings)
	if err != nil {
		return nil, err
	}
	hinfo, err := opts.Harness.Probe(ctx)
	if err != nil {
		return nil, fmt.Errorf("harness %s is not usable: %w", cfg.Harness.Name, err)
	}
	repo := cmp.Or(opts.RepoPath, om.Repository.Path)
	st, err := source.Inspect(ctx, repo)
	if err != nil {
		return nil, err
	}
	if err := source.HasCommit(ctx, st.Root, om.Repository.Commit); err != nil {
		return nil, fmt.Errorf("cannot verify run %s: %w", v.orig.ID, err)
	}
	if st.Dirty() && cfg.Audit.DirtyPolicy == contract.DirtyRefuse {
		return nil, source.DirtyError(st)
	}
	if err := checkSeparate(st.Root, opts.Store.Root); err != nil {
		return nil, err
	}
	defs, err := agent.Load(cfg.Agents.Dir, validatorAgent)
	if err != nil {
		return nil, err
	}
	// An older or custom Pruv would re-check the finding as if it were
	// validating it, reading a changed excerpt as misquoted evidence.
	if pruv := defs[validatorAgent]; !strings.Contains(pruv.Instructions, verificationSection) {
		return nil, fmt.Errorf("agent pruv (%s, version %s) has no %q section; verify needs Pruv version 3 or later", pruv.Source, pruv.Version, verificationSection)
	}
	schemas, err := agentSchemas()
	if err != nil {
		return nil, err
	}
	before, err := source.Fingerprint(ctx, st.Root)
	if err != nil {
		return nil, err
	}
	runCost, err := preflight(ctx, opts, cost.ForVerify(cfg, len(v.items), enforcesBudget(hinfo)))
	if err != nil {
		return nil, err
	}
	a := &auditor{opts: opts, defs: defs, schemas: schemas, verify: v}
	return a.launch(ctx, "verifying "+v.orig.ID+" in", st, hinfo, runCost, before)
}

// loadOriginal resolves the audit run to verify and selects its findings,
// refusing anything ADR 0006 §8 rules out.
func loadOriginal(store storage.Store, runID string, ids []string) (*verifier, *contract.Manifest, error) {
	var orig *storage.Run
	var err error
	if runID == "" {
		orig, err = store.LatestAudit()
	} else {
		orig, err = store.Open(runID)
	}
	if err != nil {
		return nil, nil, err
	}
	om, err := orig.Manifest()
	if err != nil {
		return nil, nil, err
	}
	if om.Verifies != nil {
		return nil, nil, fmt.Errorf("run %s is a verify run; verify the audit run it checks (%s) instead", orig.ID, om.Verifies.Run)
	}
	if status, note := storage.EffectiveStatus(om); status == contract.RunRunning {
		return nil, nil, fmt.Errorf("run %s is still running (%s); verify it after it finishes", orig.ID, note)
	}
	if !orig.Exists(storage.FindingsFile) {
		return nil, nil, fmt.Errorf("run %s has no %s because its validation did not finish; there are no established findings to verify", orig.ID, storage.FindingsFile)
	}
	if !orig.Exists(storage.SourceDir) {
		return nil, nil, fmt.Errorf("run %s has no %s snapshot to compare against", orig.ID, storage.SourceDir)
	}
	raw, err := os.ReadFile(orig.Path(storage.FindingsFile))
	if err != nil {
		return nil, nil, err
	}
	data, err := report.Load(orig)
	if err != nil {
		return nil, nil, err
	}
	items, err := selectFindings(orig.ID, data.Items, ids)
	if err != nil {
		return nil, nil, err
	}
	v := &verifier{orig: orig, items: items}
	if data.Discovery != nil {
		v.discovery = &data.Discovery.Report
	}
	v.target = contract.VerifyTarget{
		Run: orig.ID, SourceRevision: om.Repository.SourceRevision, SnapshotDigest: om.Repository.SnapshotDigest,
		FindingsSHA256: sha256Hex(raw),
	}
	for _, it := range items {
		v.target.Findings = append(v.target.Findings, it.ID)
	}
	return v, om, nil
}

func established(s contract.ValidationState) bool {
	return s == contract.StateConfirmed || s == contract.StateSupportedRisk
}

// selectFindings returns the findings to verify, sorted by ID: those named
// in ids, or every established finding when ids is empty.
func selectFindings(runID string, items []report.Item, ids []string) ([]report.Item, error) {
	var out []report.Item
	if len(ids) == 0 {
		for _, it := range items {
			if established(it.State) {
				out = append(out, it)
			}
		}
	} else {
		byID := map[string]report.Item{}
		for _, it := range items {
			byID[it.ID] = it
		}
		for _, id := range slices.Compact(slices.Sorted(slices.Values(ids))) {
			it, ok := byID[id]
			switch {
			case !ok:
				return nil, fmt.Errorf("run %s has no finding %s", runID, id)
			case !established(it.State):
				return nil, fmt.Errorf("%s is %s; only confirmed or supported_risk findings can be verified", id, it.State)
			}
			out = append(out, it)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("run %s has no confirmed or supported_risk findings to verify", runID)
	}
	slices.SortFunc(out, func(a, b report.Item) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// writeFindings copies the selected findings, unchanged, into the verify
// run, which makes it self-contained (ADR 0006 §1).
func (v *verifier) writeFindings(run *storage.Run) error {
	set := &contract.FindingSet{Header: contract.NewHeader(contract.KindFindings, run.ID)}
	for _, it := range v.items {
		set.Findings = append(set.Findings, it.Finding)
	}
	return run.WriteJSON(storage.FindingsFile, set)
}

// status derives a verify run's outcome: any unverified finding makes it
// partial, and a run that verified nothing has failed.
func (v *verifier) status(m contract.Manifest) contract.RunStatus {
	unverified := len(v.items)
	if v.log != nil {
		recs := v.log.decided()
		unverified = len(v.items) - len(recs)
		for _, r := range recs {
			if r.Outcome == contract.OutcomeUnverified {
				unverified++
			}
		}
	}
	t := m.Task(taskVerification)
	switch {
	case unverified == len(v.items):
		return contract.RunFailed
	case unverified > 0 || t == nil || t.Status != contract.TaskSucceeded:
		return contract.RunPartial
	}
	return contract.RunCompleted
}

func (a *auditor) runVerification(ctx context.Context) error {
	v := a.verify
	run := a.run
	log, err := newRecordLog(len(v.items), func(recs []contract.VerificationRecord) error {
		return run.WriteJSON(storage.VerificationFile, &contract.VerificationSet{Header: contract.NewHeader(contract.KindVerification, run.ID), Records: recs})
	})
	if err != nil {
		return err
	}
	v.log = log
	if err := a.update(func(m *contract.Manifest) error {
		t := m.Task(taskVerification)
		t.Artifacts = append(t.Artifacts, storage.FindingsFile, storage.VerificationFile)
		return nil
	}); err != nil {
		return err
	}
	identical := a.manifest().Repository.SnapshotDigest == v.target.SnapshotDigest
	if identical {
		a.logf("[verification] snapshot is identical to run %s; no agent will run", v.orig.ID)
	}
	tasks := make([]Task, len(v.items))
	for i, it := range v.items {
		tasks[i] = Task{ID: it.ID, Run: func(ctx context.Context) error {
			rec, err := a.verifyFinding(ctx, it, identical)
			log.record(i, rec)
			return err
		}}
	}
	var failed []error
	collect := func(id string, status contract.TaskStatus, err error) {
		if status == contract.TaskFailed {
			failed = append(failed, fmt.Errorf("%s: %w", id, err))
		}
	}
	if err := Schedule(ctx, tasks, a.opts.Config.Audit.MaxParallelAgents, collect); err != nil {
		return err
	}
	if err := log.err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d findings could not be verified: %w", len(failed), len(v.items), errors.Join(failed...))
	}
	return nil
}

// verifyFinding re-checks one finding at the new snapshot and decides its
// outcome with VerifyGate. The record is complete even when Pruv fails;
// the error says why.
func (a *auditor) verifyFinding(ctx context.Context, it report.Item, identical bool) (contract.VerificationRecord, error) {
	v := a.verify
	pruv := a.defs[validatorAgent]
	rec := contract.VerificationRecord{
		FindingID: it.ID, OriginalState: it.State, Severity: it.Severity, Confidence: it.Confidence,
		Relocations: relocateAll(a.snapshot, it.Evidence),
		Validator:   pruv.Name, ValidatorVersion: pruv.Version,
	}
	if identical {
		rec.Outcome, rec.OutcomeReason = contract.OutcomeStillPresent, identicalReason
		a.logf("[verification] %s → %s", it.ID, rec.Outcome)
		return rec, nil
	}
	prompt := verifyPrompt(v.target, a.revision, it, rec.Relocations, v.discovery, a.limits(validatorAgent))
	dec, err := execute[contract.ValidationDecision](ctx, a, taskVerification, path.Join(taskVerification, it.ID), pruv, prompt, a.schemas[validatorAgent])
	if err != nil {
		rec.Error = err.Error()
	} else {
		rec.Decision = &dec
		rec.AdditionalChecks = relocateAll(a.snapshot, dec.AdditionalEvidence)
		origSnapshot := v.orig.Path(storage.SourceDir)
		for _, e := range dec.AdditionalEvidence {
			rec.AdditionalPriorMatches = append(rec.AdditionalPriorMatches, findings.PriorMatches(origSnapshot, e))
		}
	}
	rec.Outcome, rec.OutcomeReason = findings.VerifyGate(rec.Relocations, rec.Decision, rec.AdditionalChecks, rec.AdditionalPriorMatches)
	a.logf("[verification] %s → %s", it.ID, rec.Outcome)
	return rec, err
}

// relocateAll checks every evidence item against a snapshot, by index.
func relocateAll(snapshot string, evidence []contract.Evidence) []contract.Relocation {
	out := make([]contract.Relocation, len(evidence))
	for i, e := range evidence {
		out[i] = findings.Relocate(snapshot, e)
		out[i].Index = i
	}
	return out
}
