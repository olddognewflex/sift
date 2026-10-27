# ADR 0004: Deterministic plan and reports; Go types as the contract source

**Status:** accepted

## Decisions

**Fixed task graph.** Discovery → plan → reviewers → validation → report
is declared in code. No agent decides which step runs next. Reviewers run
in parallel (bounded by `max_parallel_agents`, default 2), as do Pruv
executions. Each is an independent multi-minute process, so parallelism
roughly halves wall-clock time. No other concurrency exists.

**Deterministic review plan.** The README's "Risk & Scope Plan" stage is
implemented in code for the MVP: one task per configured reviewer, focused
on the critical paths found by discovery. An LLM planner would add cost and
nondeterminism. That's not worth it with two reviewers.

**Reports rendered by code, not an agent.** The README's "Report
Synthesizer" is a template over validated artifacts. Every statement in a
report traces to an artifact, so a report can't introduce unsupported
claims, and it can be regenerated after a crash (`sift report
--regenerate`). No numeric scorecard is produced, because the MVP collects
no measurements to justify scores. The report says so instead of inventing
values.

**Go types are the single contract definition.** `internal/contract` types
drive strict decoding, `Validate()`, the JSON Schemas passed to the harness
(via a small reflector in `internal/schema`), and the published
`schemas/*.json` (a test checks they're current). Enumerations are defined
once, in an `Enum()` method used by both validation and schema generation.
A tailored ~150-line reflector was chosen over a schema library: it emits
exactly the draft-07 subset Claude Code accepts, and it adds no dependency.

## Consequences

- Executive summaries are structured rather than narrative.
- Adding a reviewer means adding an agent definition, a `Category` value,
  and a config name. The orchestrator is unchanged.
