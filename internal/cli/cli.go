// Package cli implements the sift command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/olddognewflex/sift/internal/audit"
	"github.com/olddognewflex/sift/internal/config"
	"github.com/olddognewflex/sift/internal/contract"
	"github.com/olddognewflex/sift/internal/harness"
	"github.com/olddognewflex/sift/internal/harness/claudecode"
	"github.com/olddognewflex/sift/internal/harness/kiro"
	"github.com/olddognewflex/sift/internal/report"
	"github.com/olddognewflex/sift/internal/storage"
)

// Exit codes. A run that finishes with incomplete results is not a success.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitPartial     = 2
	ExitInterrupted = 130
)

// ExitError carries a process exit code.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string { return e.Err.Error() }
func (e *ExitCodeError) Unwrap() error { return e.Err }

// Deps are the CLI's external dependencies, replaceable in tests.
type Deps struct {
	Stdout, Stderr io.Writer
	Version        string
	// NewHarness builds the configured harness.
	NewHarness func(config.Config) (harness.Harness, error)
	Now        func() time.Time
}

// DefaultHarness builds the harness named in the configuration.
func DefaultHarness(cfg config.Config) (harness.Harness, error) {
	switch cfg.Harness.Name {
	case claudecode.Name:
		return claudecode.New(cfg.Harness.Binary), nil
	case kiro.Name:
		return kiro.New(cfg.Harness.Binary), nil
	}
	return nil, fmt.Errorf("unsupported harness %q", cfg.Harness.Name)
}

// New returns the root command.
func New(d Deps) *cobra.Command {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewHarness == nil {
		d.NewHarness = DefaultHarness
	}
	var configPath string
	root := &cobra.Command{
		Use:           "sift",
		Short:         "Evidence-driven software engineering audits",
		Long:          "Sift audits a Git repository with specialist AI reviewers and accepts a finding only after an independent validator (Pruv) and Sift's own evidence checks support it.",
		Version:       d.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(d.Stdout)
	root.SetErr(d.Stderr)
	root.PersistentFlags().StringVar(&configPath, "config", "", "configuration file (default: ./sift.yaml if present, else built-in defaults)")

	load := func() (config.Config, storage.Store, error) {
		cfg, _, err := config.Load(configPath)
		if err != nil {
			return cfg, storage.Store{}, err
		}
		dir, err := cfg.RunsDir()
		return cfg, storage.Store{Root: dir}, err
	}

	root.AddCommand(initCmd(d, &configPath), auditCmd(d, load), statusCmd(d, load), findingsCmd(d, load), reportCmd(d, load))
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, d Deps, args []string) int {
	cmd := New(d)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(d.Stderr, "sift:", err)
	var ec *ExitCodeError
	if errors.As(err, &ec) {
		return ec.Code
	}
	return ExitError
}

type loader func() (config.Config, storage.Store, error)

// ---------------------------------------------------------------------------

func initCmd(d Deps, configPath *string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a documented default configuration file",
		Long:  "Writes sift.yaml (or the --config path) with every option documented and set to its default.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := *configPath
			if path == "" {
				path = config.DefaultFile
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; use --force to overwrite", path)
			}
			if err := os.WriteFile(path, []byte(config.Template), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(d.Stdout, "Wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

func auditCmd(d Deps, load loader) *cobra.Command {
	var dirty, model string
	cmd := &cobra.Command{
		Use:   "audit <path>",
		Short: "Audit a Git repository",
		Long: `Runs a full audit: records the repository state, exports a read-only snapshot,
runs discovery, the configured reviewers, and Pruv validation, then writes reports.

Exit status: 0 completed, 2 partial (some stages failed; results incomplete),
130 interrupted, 1 failed or could not start.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, store, err := load()
			if err != nil {
				return err
			}
			if dirty != "" {
				cfg.Audit.DirtyPolicy = contract.DirtyPolicy(dirty)
			}
			if model != "" {
				cfg.Harness.Model = model
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			h, err := d.NewHarness(cfg)
			if err != nil {
				return err
			}
			res, err := audit.Run(cmd.Context(), audit.Options{
				RepoPath: args[0], Config: cfg, Harness: h, Store: store,
				SiftVersion: d.Version, Log: d.Stderr, Now: d.Now,
			})
			if res == nil {
				return err
			}
			m := res.Manifest
			fmt.Fprintf(d.Stdout, "\nRun:     %s\nStatus:  %s\nRevision: %s\n", res.Run.ID, m.Status, m.Repository.SourceRevision)
			if paths, perr := report.Paths(res.Run); perr == nil {
				fmt.Fprintf(d.Stdout, "Reports: %s\n", filepath.Dir(paths[0]))
			}
			fmt.Fprintf(d.Stdout, "Details: sift status --run %s\n", res.Run.ID)
			switch {
			case err != nil:
				return err
			case m.Status == contract.RunInterrupted:
				return &ExitCodeError{ExitInterrupted, errors.New("audit interrupted; partial results were saved")}
			case m.Status == contract.RunPartial:
				return &ExitCodeError{ExitPartial, errors.New("audit finished with incomplete results (see status)")}
			case m.Status != contract.RunCompleted:
				return fmt.Errorf("audit %s", m.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dirty, "dirty", "", "dirty working tree policy: refuse, head, or include (overrides config)")
	cmd.Flags().StringVar(&model, "model", "", "model passed to the harness (overrides config)")
	return cmd
}

func statusCmd(d Deps, load loader) *cobra.Command {
	var runID string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the status of an audit run (default: the latest)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, store, err := load()
			if err != nil {
				return err
			}
			run, err := store.Resolve(runID)
			if err != nil {
				return err
			}
			m, err := run.Manifest()
			if err != nil {
				return err
			}
			status, note := effectiveStatus(m)
			execs := loadExecutions(run, m)
			if asJSON {
				return writeJSON(d.Stdout, map[string]any{"effective_status": status, "note": note, "manifest": m, "executions": execs})
			}
			printStatus(d.Stdout, run, m, status, note, execs)
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "run ID (default: latest run)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func findingsCmd(d Deps, load loader) *cobra.Command {
	var runID, state string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "findings",
		Short: "List findings with category, severity, confidence and validation state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state != "" && !contract.Valid(contract.ValidationState(state)) {
				return fmt.Errorf("--state must be one of %v", contract.ValidationState("").Enum())
			}
			_, store, err := load()
			if err != nil {
				return err
			}
			run, err := store.Resolve(runID)
			if err != nil {
				return err
			}
			data, err := report.Load(run)
			if err != nil {
				return err
			}
			var items []report.Item
			for _, it := range data.Items {
				if state == "" || string(it.State) == state {
					items = append(items, it)
				}
			}
			if asJSON {
				if items == nil {
					items = []report.Item{}
				}
				return writeJSON(d.Stdout, items)
			}
			if data.Manifest.Status != contract.RunCompleted {
				fmt.Fprintf(d.Stdout, "Note: run %s is %s; findings may be incomplete.\n\n", run.ID, data.Manifest.Status)
			}
			if len(items) == 0 {
				fmt.Fprintln(d.Stdout, "No findings.")
				return nil
			}
			tw := tabwriter.NewWriter(d.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCATEGORY\tSEVERITY\tCONFIDENCE\tSTATE\tTITLE")
			for _, it := range items {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, it.Category, it.Severity, it.Confidence, it.State, it.Title)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "run ID (default: latest run)")
	cmd.Flags().StringVar(&state, "state", "", "only show findings in this validation state")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func reportCmd(d Deps, load loader) *cobra.Command {
	var runID string
	var regenerate bool
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show report locations, or regenerate reports from persisted artifacts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, store, err := load()
			if err != nil {
				return err
			}
			run, err := store.Resolve(runID)
			if err != nil {
				return err
			}
			if regenerate {
				if _, err := report.Generate(run, d.Now); err != nil {
					return err
				}
			}
			paths, err := report.Paths(run)
			if err != nil {
				if !regenerate && errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("run %s has no reports; generate them with `sift report --run %s --regenerate`", run.ID, run.ID)
				}
				return err
			}
			for _, p := range paths {
				fmt.Fprintln(d.Stdout, p)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "run ID (default: latest run)")
	cmd.Flags().BoolVar(&regenerate, "regenerate", false, "rebuild reports from the run's artifacts")
	return cmd
}

// ---------------------------------------------------------------------------

// effectiveStatus detects runs left "running" by a process that no longer
// exists (killed, crashed, machine restarted).
func effectiveStatus(m *contract.Manifest) (contract.RunStatus, string) {
	if m.Status != contract.RunRunning {
		return m.Status, ""
	}
	host, _ := os.Hostname()
	if m.Host == host && !processAlive(m.PID) {
		return contract.RunInterrupted, fmt.Sprintf("the audit process (pid %d) exited without finishing; artifacts written so far are preserved", m.PID)
	}
	if m.Host != host {
		return m.Status, fmt.Sprintf("run is executing on host %s", m.Host)
	}
	return m.Status, fmt.Sprintf("in progress (pid %d)", m.PID)
}

func loadExecutions(run *storage.Run, m *contract.Manifest) []contract.ExecutionResult {
	var out []contract.ExecutionResult
	for _, t := range m.Tasks {
		for _, rel := range t.Executions {
			var e contract.ExecutionResult
			if run.ReadJSON(rel, &e) == nil {
				out = append(out, e)
			}
		}
	}
	return out
}

func printStatus(w io.Writer, run *storage.Run, m *contract.Manifest, status contract.RunStatus, note string, execs []contract.ExecutionResult) {
	r := m.Repository
	fmt.Fprintf(w, "Run:        %s\n", run.ID)
	fmt.Fprintf(w, "Repository: %s\n", r.Path)
	rev := r.SourceRevision
	if r.Branch != "" {
		rev += " (" + r.Branch + ")"
	}
	fmt.Fprintf(w, "Revision:   %s\n", rev)
	if r.Dirty {
		fmt.Fprintf(w, "Dirty tree: %d uncommitted path(s), policy %s\n", len(r.StatusPorcelain), r.DirtyPolicy)
	}
	fmt.Fprintf(w, "Status:     %s", status)
	if note != "" {
		fmt.Fprintf(w, " (%s)", note)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Started:    %s\n", m.CreatedAt.Local().Format(time.RFC3339))
	if m.FinishedAt != nil {
		fmt.Fprintf(w, "Finished:   %s (%s)\n", m.FinishedAt.Local().Format(time.RFC3339), m.FinishedAt.Sub(m.CreatedAt).Round(time.Second))
	}
	model := m.Harness.Model
	if model == "" {
		model = "harness default"
	}
	fmt.Fprintf(w, "Harness:    %s %s, model %s\n", m.Harness.Name, m.Harness.Version, model)

	fmt.Fprintln(w, "\nTasks:")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, t := range m.Tasks {
		dur := ""
		if t.StartedAt != nil && t.EndedAt != nil {
			dur = t.EndedAt.Sub(*t.StartedAt).Round(time.Second).String()
		}
		detail := ""
		if len(t.Executions) > 0 {
			detail = fmt.Sprintf("%d agent execution(s)", len(t.Executions))
		}
		if t.Error != "" {
			detail = strings.TrimSpace(detail + " " + firstLine(t.Error))
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", t.ID, t.Status, dur, detail)
	}
	tw.Flush()

	var cost float64
	var denials int
	for _, e := range execs {
		cost += e.CostUSD
		denials += e.PermissionDenials
	}
	fmt.Fprintf(w, "\nAgent executions: %d, reported cost $%.2f, denied tool calls %d\n", len(execs), cost, denials)
	if data, err := report.Load(run); err == nil && len(data.Items) > 0 {
		counts := map[contract.ValidationState]int{}
		for _, it := range data.Items {
			counts[it.State]++
		}
		var parts []string
		for _, s := range contract.ValidationState("").Enum() {
			if n := counts[contract.ValidationState(s)]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, s))
			}
		}
		fmt.Fprintf(w, "Findings:   %s\n", strings.Join(parts, ", "))
	}
	if m.Integrity != nil {
		fmt.Fprintf(w, "Integrity:  repository unchanged = %t\n", m.Integrity.RepositoryUnchanged)
	}
	if len(m.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, wn := range m.Warnings {
			fmt.Fprintf(w, "  - %s\n", wn)
		}
	}
	if paths, err := report.Paths(run); err == nil {
		fmt.Fprintf(w, "\nReports:    %s\n", filepath.Dir(paths[0]))
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
