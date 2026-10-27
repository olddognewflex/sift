# ADR 0002: Claude Code as the first harness, enforced read-only

**Status:** accepted

## Context

Sift must stay independent of any one agent platform, but the MVP needs one
real adapter. Claude Code 2.1.311 is installed on the development machine.
Codex is not.

## Decision

The `harness.Harness` interface has two methods: `Probe` (verify
installation and capabilities) and `Run` (execute one agent and return
schema-conforming structured output plus metadata). Retries, scheduling,
artifacts, and validation stay in the orchestrator.

The Claude Code adapter runs:

```text
claude --print --output-format json --json-schema <schema>
       --tools Read,Grep,Glob --restricted --safe-mode --strict-mcp-config
       --permission-mode dontAsk --permission-prompts none
       --no-session-persistence --system-prompt <agent definition>
       --max-budget-usd <limit> [--model <model>]
```

It runs with the snapshot as its working directory, takes the prompt on
stdin, runs in its own process group, gets SIGTERM on cancel or timeout and
SIGKILL after a 10s grace period, and has any leftover group members killed.

### Verified behavior (Claude Code 2.1.311, 2026-10-27)

Each behavior was checked with a live call before it was relied on:

- `--output-format json` returns one result object with `structured_output`,
  `is_error`, `subtype`, `session_id`, `total_cost_usd`, `num_turns`,
  `permission_denials`, and `modelUsage`.
- `--json-schema` is enforced. Optional properties and `enum` work. A draft
  2020-12 `$schema` URI is **rejected** ("no schema with key or ref"), so
  Sift emits draft-07.
- A prompt on stdin works with `--print`.
- `--tools Read,Grep,Glob` exposes no write, shell, or web tools. Asked to
  create a file or run `touch`, the agent reported it had no such tools.
- `--restricted` confines file tools to the working directory. A read of
  `../outside.txt` was denied and appeared in `permission_denials`.
- With `--system-prompt`, `--restricted`, and `--safe-mode`, a `CLAUDE.md`
  planted in the working directory was not in the agent's context.
  `--safe-mode` also disables user hooks and plugins, so local
  customizations can't alter agent behavior.
- Budget exhaustion returns `is_error: true`,
  `subtype: error_max_budget_usd`, and exit code 1, with JSON still on
  stdout.

`Probe` checks that every flag above still appears in `claude --help`, and
fails the audit before a run is created if one is missing.

`make conformance` re-checks these behaviors live with the harness
conformance suite (`internal/harness/conformance`). It passed on
2026-10-27 with Claude Code 2.1.311 and `haiku`.

Negative controls ran the same suite through a wrapper that drops flags.
They show the suite has teeth and that the isolation flags overlap:

| Flags dropped | Result |
|---|---|
| `--restricted`, `--safe-mode`, `--strict-mcp-config` | Every live check failed: a planted MCP server ran, the planted `CLAUDE.md` was loaded, the user's plugins and hooks loaded, and detached plugin processes outlived the run |
| `--restricted`, `--safe-mode` | Failed: planted `CLAUDE.md` loaded, planted hook or MCP server ran, processes left behind |
| `--restricted` only | Passed: out-of-snapshot reads were still denied by `--permission-mode dontAsk` |
| `--safe-mode` only, or with `--strict-mcp-config` | Passed: `--restricted` alone kept project config out |
| `--strict-mcp-config` only | Passed |

So project-config isolation holds if either `--restricted` or `--safe-mode`
is present, and read confinement holds if either `--restricted` or
`dontAsk` is present. Sift keeps all of them as defense in depth.

## Guarantees

**Enforced by the harness:** a tool allowlist with no shell, write, or web
access; file access confined to the snapshot; permission prompts denied
instead of granted; no project or user settings, hooks, plugins, CLAUDE.md,
or MCP servers; a per-execution spend limit. **Enforced by Sift:** a
wall-clock timeout, the snapshot outside the repository, read-only snapshot
files, and the before/after repository fingerprint.

**Not guaranteed:** there is no OS-level sandbox. The harness process runs
with the user's privileges and network access, which it needs to reach the
model API. Path confinement is the harness's own implementation; it was
verified empirically, not formally. These limits are recorded in every
manifest (`policy.unenforced`) and every report.

## Consequences

- No agent can execute code, so the MVP produces only source evidence.
  Execution evidence needs a sandboxed command policy first.
- Running `--bare` would cut more context but needs an `ANTHROPIC_API_KEY`
  (OAuth is not read). `--safe-mode` achieves the isolation that matters
  without it.
- A second adapter (Kiro: ADR 0005) implements the same two methods.
  Nothing in the orchestrator changes. It must pass the conformance suite
  before Sift relies on it; a guarantee it cannot meet (such as a budget
  limit) is listed in `Info.Unenforced` and its check is skipped.
