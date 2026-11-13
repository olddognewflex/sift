package bench

import (
	"fmt"
	"maps"
	"slices"

	"github.com/olddognewflex/sift/internal/contract"
)

// RunObs is one audit run of a fixture. An incomplete run still counts
// every defect in its key, so what it failed to report scores as misses.
// The driver emits one RunObs per (fixture, trial), with Complete false
// when the audit failed, so every fixture has the same trials.
type RunObs struct {
	Trial        int           `json:"trial"`
	Complete     bool          `json:"complete"`
	Observations []Observation `json:"observations"`
}

// FixtureRuns is every run of one fixture.
type FixtureRuns struct {
	Key  Key      `json:"key"`
	Runs []RunObs `json:"runs"`
}

// Stage counts one scope of observations matched against the keys. Counts
// are summed over runs; rates are computed from the sums and are nil when
// their denominator is zero.
type Stage struct {
	Findings      int `json:"findings"`
	TP            int `json:"tp"` // distinct defects matched, any category
	DecoysFlagged int `json:"decoys_flagged"`
	Unkeyed       int `json:"unkeyed"`
	Known         int `json:"known"`
	Duplicates    int `json:"duplicates"`
	// OffCategoryTP counts matched defects that no matching finding in the
	// scope (duplicates included) reported in the defect's category.
	OffCategoryTP int `json:"off_category_tp"`
	// Defects and DefectsFound are recall's denominator and numerator: for
	// a reviewer, the defects of its own category; overall, all defects.
	Defects      int      `json:"defects"`
	DefectsFound int      `json:"defects_found"`
	Precision    *float64 `json:"precision"` // tp / (tp + decoys_flagged + unkeyed)
	Recall       *float64 `json:"recall"`
}

// Stages is a stage scored per reviewer (its own findings only) and
// overall (all findings in one scope). Candidate overall recall is the
// candidate union.
type Stages struct {
	Reviewers map[string]*Stage `json:"reviewers"`
	Overall   Stage             `json:"overall"`
}

// Loss counts items by what stopped them: the mechanism closest to
// acceptance among their matching findings, or unvalidated when any of
// those findings has no decision.
type Loss struct {
	PruvReject      int `json:"pruv_reject"`
	PruvInvestigate int `json:"pruv_investigate"`
	Gate            int `json:"gate"`
	Unvalidated     int `json:"unvalidated"` // always 0 for decoys
}

// Pruv scores validation: what it kept of the defects reviewers found and
// what it stopped of the decoys they flagged.
type Pruv struct {
	Precision       *float64 `json:"precision"` // = final overall
	Recall          *float64 `json:"recall"`    // = final overall
	DefectsFound    int      `json:"defects_found"`
	DefectsAccepted int      `json:"defects_accepted"`
	Retention       *float64 `json:"retention"` // defects_accepted / defects_found
	DefectsLostBy   Loss     `json:"defects_lost_by"`
	DecoysFlagged   int      `json:"decoys_flagged"`
	// A flagged decoy is rejected when every matching finding has a
	// decision and none was accepted; with no acceptance but an
	// unvalidated match it is unresolved and left out of the rate.
	DecoysRejected     int      `json:"decoys_rejected"`
	DecoysRejectedBy   Loss     `json:"decoys_rejected_by"`
	DecoysAccepted     int      `json:"decoys_accepted"`
	DecoysUnresolved   int      `json:"decoys_unresolved"`
	DecoysNotFlagged   int      `json:"decoys_not_flagged"`
	DecoyRejectionRate *float64 `json:"decoy_rejection_rate"`
	Unvalidated        int      `json:"unvalidated"` // findings without a decision
}

// Summary scores a set of runs.
type Summary struct {
	Runs           int `json:"runs"`
	IncompleteRuns int `json:"incomplete_runs"`
	// Unattributed counts findings whose reporter is not a registered
	// reviewer: in no reviewer's stage, but in the overall ones.
	Unattributed int    `json:"unattributed"`
	Candidate    Stages `json:"candidate"`
	Final        Stages `json:"final"` // accepted findings only
	Pruv         Pruv   `json:"pruv"`
}

// Range is the spread of one rate across trials.
type Range struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Scores is the benchmark score: aggregate, per fixture, and, with more
// than one trial, each rate's min and max across per-trial aggregates
// (keyed "candidate.correctness.precision", "pruv.retention", ...).
type Scores struct {
	RulesVersion string             `json:"rules_version"`
	Trials       int                `json:"trials"`
	Aggregate    Summary            `json:"aggregate"`
	Fixtures     map[string]Summary `json:"fixtures"`
	TrialRange   map[string]Range   `json:"trial_range,omitempty"`
}

// Score scores every run of every fixture. It is pure and deterministic.
// Every fixture must have the same set of trial numbers, each once, or
// per-trial aggregates would not be comparable.
func Score(in []FixtureRuns) (Scores, error) {
	if err := checkTrials(in); err != nil {
		return Scores{}, err
	}
	agg, fixtures, trials := newSummary(), map[string]*Summary{}, map[int]*Summary{}
	for _, fr := range in {
		if fixtures[fr.Key.Fixture] == nil {
			fixtures[fr.Key.Fixture] = newSummary()
		}
		for _, run := range fr.Runs {
			if trials[run.Trial] == nil {
				trials[run.Trial] = newSummary()
			}
			for _, s := range []*Summary{agg, fixtures[fr.Key.Fixture], trials[run.Trial]} {
				s.add(fr.Key, run)
			}
		}
	}
	out := Scores{RulesVersion: RulesVersion, Trials: len(trials), Fixtures: map[string]Summary{}}
	agg.finish()
	out.Aggregate = *agg
	for name, s := range fixtures {
		s.finish()
		out.Fixtures[name] = *s
	}
	if len(trials) < 2 {
		return out, nil
	}
	out.TrialRange = map[string]Range{}
	for _, s := range trials {
		s.finish()
		for name, r := range s.rates() {
			if r == nil {
				continue
			}
			cur, ok := out.TrialRange[name]
			if !ok {
				cur = Range{*r, *r}
			}
			out.TrialRange[name] = Range{min(cur.Min, *r), max(cur.Max, *r)}
		}
	}
	return out, nil
}

func checkTrials(in []FixtureRuns) error {
	byFixture := map[string][]int{}
	for _, fr := range in {
		for _, run := range fr.Runs {
			if slices.Contains(byFixture[fr.Key.Fixture], run.Trial) {
				return fmt.Errorf("fixture %s: trial %d appears more than once", fr.Key.Fixture, run.Trial)
			}
			byFixture[fr.Key.Fixture] = append(byFixture[fr.Key.Fixture], run.Trial)
		}
	}
	names := slices.Sorted(maps.Keys(byFixture))
	for _, name := range names {
		slices.Sort(byFixture[name])
		if !slices.Equal(byFixture[name], byFixture[names[0]]) {
			return fmt.Errorf("fixtures %s and %s have different trials (%v, %v); emit a run per fixture and trial", names[0], name, byFixture[names[0]], byFixture[name])
		}
	}
	return nil
}

func newSummary() *Summary {
	s := &Summary{Candidate: Stages{Reviewers: map[string]*Stage{}}, Final: Stages{Reviewers: map[string]*Stage{}}}
	for _, r := range contract.ReviewerNames() {
		s.Candidate.Reviewers[r], s.Final.Reviewers[r] = &Stage{}, &Stage{}
	}
	return s
}

func (s *Summary) add(key Key, run RunObs) {
	s.Runs++
	if !run.Complete {
		s.IncompleteRuns++
	}
	obs := run.Observations
	accepted := filter(obs, Observation.Accepted)
	s.Unattributed += len(filter(obs, func(o Observation) bool { return s.Candidate.Reviewers[o.ReportedBy] == nil }))
	for r, st := range s.Candidate.Reviewers {
		mine := func(o Observation) bool { return o.ReportedBy == r }
		st.tally(key, Match(key, filter(obs, mine)), contract.Category(r))
		s.Final.Reviewers[r].tally(key, Match(key, filter(accepted, mine)), contract.Category(r))
	}
	all := Match(key, obs)
	s.Candidate.Overall.tally(key, all, "")
	s.Final.Overall.tally(key, Match(key, accepted), "")
	s.Pruv.tally(key, all)
}

func (s *Summary) finish() {
	for _, st := range []*Stages{&s.Candidate, &s.Final} {
		st.Overall.finish()
		for _, r := range st.Reviewers {
			r.finish()
		}
	}
	p := &s.Pruv
	p.Precision, p.Recall = s.Final.Overall.Precision, s.Final.Overall.Recall
	p.Retention = ratio(p.DefectsAccepted, p.DefectsFound)
	p.DecoyRejectionRate = ratio(p.DecoysRejected, p.DecoysFlagged-p.DecoysUnresolved)
}

func (s *Summary) rates() map[string]*float64 {
	m := map[string]*float64{"pruv.retention": s.Pruv.Retention, "pruv.decoy_rejection_rate": s.Pruv.DecoyRejectionRate}
	for name, st := range map[string]Stages{"candidate": s.Candidate, "final": s.Final} {
		m[name+".overall.precision"], m[name+".overall.recall"] = st.Overall.Precision, st.Overall.Recall
		for r, rs := range st.Reviewers {
			m[name+"."+r+".precision"], m[name+"."+r+".recall"] = rs.Precision, rs.Recall
		}
	}
	return m
}

// tally counts one scope's assignments (from one Match call). cat
// restricts recall to one reviewer's category; "" means all defects.
func (s *Stage) tally(key Key, as []Assignment, cat contract.Category) {
	inScope := func(c contract.Category) bool { return cat == "" || c == cat }
	for _, e := range key.Entries {
		if e.Kind == KindDefect && inScope(e.Category) {
			s.Defects++
		}
	}
	inCategory := map[string]bool{}
	for _, a := range as {
		inCategory[a.EntryID] = inCategory[a.EntryID] || a.CategoryMatch
	}
	s.Findings += len(as)
	for _, a := range as {
		switch a.Outcome {
		case OutcomeTP:
			s.TP++
			if !inCategory[a.EntryID] {
				s.OffCategoryTP++
			}
			if inScope(a.EntryCategory) {
				s.DefectsFound++
			}
		case OutcomeDecoy:
			s.DecoysFlagged++
		case OutcomeUnkeyed:
			s.Unkeyed++
		case OutcomeKnown:
			s.Known++
		case OutcomeDuplicate:
			s.Duplicates++
		}
	}
}

func (s *Stage) finish() {
	s.Precision = ratio(s.TP, s.TP+s.DecoysFlagged+s.Unkeyed)
	s.Recall = ratio(s.DefectsFound, s.Defects)
}

// tally scores validation from the assignments of all of a run's
// observations.
func (p *Pruv) tally(key Key, all []Assignment) {
	byEntry := map[string][]Observation{}
	for _, a := range all {
		byEntry[a.EntryID] = append(byEntry[a.EntryID], a.Observation)
		if undecided(a.Observation) {
			p.Unvalidated++
		}
	}
	for _, e := range key.Entries {
		ms := byEntry[e.ID]
		accepted := slices.ContainsFunc(ms, Observation.Accepted)
		switch {
		case e.Kind == KindDefect && len(ms) > 0:
			p.DefectsFound++
			if accepted {
				p.DefectsAccepted++
			} else {
				p.DefectsLostBy.count(ms)
			}
		case e.Kind != KindDecoy:
		case len(ms) == 0:
			p.DecoysNotFlagged++
		default:
			p.DecoysFlagged++
			switch {
			case accepted:
				p.DecoysAccepted++
			case slices.ContainsFunc(ms, undecided):
				p.DecoysUnresolved++
			default:
				p.DecoysRejected++
				p.DecoysRejectedBy.count(ms)
			}
		}
	}
}

// count records what stopped findings ms, none of them accepted.
func (l *Loss) count(ms []Observation) {
	verdict := func(v contract.Verdict) bool {
		return slices.ContainsFunc(ms, func(o Observation) bool { return o.Verdict == v })
	}
	switch {
	case verdict(""):
		l.Unvalidated++
	case verdict(contract.VerdictAccept): // accepted by Pruv, demoted by the gate
		l.Gate++
	case verdict(contract.VerdictInvestigate):
		l.PruvInvestigate++
	default:
		l.PruvReject++
	}
}

func undecided(o Observation) bool { return o.Verdict == "" }

func filter(obs []Observation, keep func(Observation) bool) []Observation {
	var out []Observation
	for _, o := range obs {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	r := float64(n) / float64(d)
	return &r
}
