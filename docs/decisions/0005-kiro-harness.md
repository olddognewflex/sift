# ADR 0005: Kiro CLI harness, isolated by construction

**Status:** accepted

## Context

Sift needs a second harness to show the `harness.Harness` boundary holds.
Kiro CLI 2.34.2 is installed on the development machine. Unlike Claude Code
(ADR 0002), it has no read-only mode, no safe mode, no schema enforcement,
and no spend limit, so the adapter has to build isolation itself.

## Findings (kiro-cli 2.34.2, engine v2, 2026-10-27)

Each was observed in a live call before the design relied on it. Run in the
snapshot with an agent definition alone, Kiro is **not** isolated:

- `--agent` takes a name, not a path. Agents are found in `$KIRO_HOME/agents`
  and `<cwd>/.kiro/agents`, and **a workspace agent with the same name
  replaces the global one**. A planted `.kiro/agents/<name>.json` granted
  every tool, and its shell tool ran without approval.
- `AGENTS.md` and `.kiro/steering/*.md` in the cwd are loaded as
  instructions for custom agents, even with `"resources": []`.
- **An MCP server in `<cwd>/.kiro/settings/mcp.json` starts even with
  `"includeMcpJson": false`.** An audited repository could run a command.
- With `read` in an agent's `tools`, reads outside the cwd succeed when the
  tool is trusted.
- `includeMcpJson` and `useLegacyMcpJson` together make the agent fail to
  load.

What does hold:

- `KIRO_HOME` relocates agents, settings and sessions. Auth is stored
  elsewhere, so a private home still works with the user's login.
- `chat.disableInheritingDefaultResources: true` stops custom agents from
  inheriting global or workspace steering, skills, and `AGENTS.md`.
- `toolsSettings.<tool>.allowedPaths` pre-approves paths. Other paths need
  approval, which `--no-interactive` refuses (`[denied] tool permission
  approval is not supported in non-interactive mode` on stderr).
- A cwd with no `.kiro`, `AGENTS.md`, or MCP config loads none of them.
- `--output-format stream-json` emits ACP events: `runFinished` carries
  `status`, `finalText` and `finalTextTruncated`; `runError` carries
  `stage` and `message`; `metadata` carries per-turn `meteringUsage` in
  credits, not dollars.
- The prompt can be given on stdin.

## Decision

The adapter (`internal/harness/kiro`) runs:

```text
KIRO_HOME=<private>/home  cwd=<private>/workspace (empty)
kiro-cli chat --no-interactive --agent-engine v2 --agent sift-<random>
              --trust-tools= --model <model> --output-format stream-json
              < prompt + output schema
```

- The agent definition is written to the private home under a random name,
  so no file in the repository or the user's `~/.kiro` can replace it. It
  exposes only `read`, `glob` and `grep`, pre-approves none of them,
  scopes all three to the snapshot's absolute path, and has no resources,
  hooks or MCP servers.
- The private home's settings set `chat.disableInheritingDefaultResources`.
- **The snapshot is never the cwd.** This is the only reliable way to keep
  the repository's `.kiro` config and MCP servers out. The agent's
  instructions get a note giving the snapshot's absolute path and asking for
  repo-relative paths in output. Sift's contract validation rejects
  absolute paths, so a violation is retried, not accepted.
- The output schema goes in the prompt. The adapter extracts the JSON object
  from `finalText` and checks it with `schema.Validate` before returning,
  so a mismatch is an `ErrInvalidOutput` (retryable).
- Timeouts, cancellation and process-group cleanup match the Claude adapter.
- `Probe` runs in an empty directory and checks that the binary is
  kiro-cli, its version, every required flag, `stream-json`, the
  `chat.disableInheritingDefaultResources` setting, and that the user is
  logged in.
- `Run` refuses to start if the private temp directory sits under a folder
  containing `.kiro` (e.g. a `TMPDIR` under `$HOME`), in case Kiro searches
  parent directories for workspace config.
- Session setup failures (`runError` at stage `init`, such as an unknown
  model) are `ErrUnsupported`, not retried.

## Verification

`make conformance` passed against kiro-cli 2.34.2 with `claude-haiku-5.5`
on 2026-10-27, including an explicit `<snapshot>/../outside/notes.txt`
read, which was denied (`allowedPaths` matches the normalized path). The
budget check is skipped because Kiro cannot enforce one,
and that is disclosed in `Info.Unenforced`.

Negative controls ran the suite through a wrapper that undoes one
protection:

| Control | Result |
|---|---|
| Run in the snapshot instead of the empty workspace | Failed: the planted MCP server ran. The code word stayed hidden because the private-home setting still blocked `AGENTS.md` and steering |
| `--trust-all-tools` instead of `--trust-tools=` | Failed: a file outside the snapshot was read and its secret appeared in the output |

## Guarantees

**Enforced by the harness:** read-only tools; reads scoped to the snapshot,
with other paths refused in non-interactive mode. **Enforced by Sift:** an
empty private workspace and private `KIRO_HOME`, a random agent name,
schema validation of the output, a wall-clock timeout, the snapshot outside
the repository, read-only snapshot files, and the repository fingerprint.

**Not guaranteed:** no spend limit (only the timeout bounds spend; Kiro
meters credits, which Sift does not record as `cost_usd`), no native schema
enforcement, and no operating-system sandbox. Path scoping is Kiro's own
implementation, verified empirically. These are recorded in every manifest
(`policy.unenforced`) and report.

## Consequences

- Model names are Kiro's (`claude-sonnet-5.5`, not `sonnet`); the default
  is Kiro's `auto`, which picks a model per task. Set `harness.model` for
  comparable runs.
- `agents.max_budget_usd` has no effect with this harness. Use
  `agents.timeout` to bound spend.
- Kiro's MCP-from-workspace behavior is a hazard for any Kiro user, not just
  Sift; the empty-workspace design does not depend on it being fixed.
