---
name: sift-audit
description: Run a Sift audit of a Git repository (architecture and correctness review with independently validated, evidence-backed findings) and summarize the results. Use when the user asks to audit, review, or "sift" a repository or the current project.
argument-hint: "[path] [--harness claude-code|kiro] [--model MODEL] [--dirty refuse|head|include]"
allowed-tools: Bash(sift audit:*), Bash(sift findings:*), Bash(sift status:*), Bash(sift report:*), Bash(git status:*), Bash(git -C:*), Bash(command -v sift), Read
---

# Sift audit

Run `sift audit` on a repository, wait for it, and report the findings Sift
accepted. Sift runs its own read-only agents and validates every finding
against the source; your job is to launch it, interpret the outcome
faithfully, and summarize. Do not edit the audited code.

Arguments: `$ARGUMENTS`

Run every command below on its own: no `;`, `&&`, pipes or redirection.
Only single `sift`, `git status` and `command -v sift` commands are
pre-approved; a chained command needs the user's permission and fails in
non-interactive sessions.

## 1. Check prerequisites

- Run `command -v sift`. If it is missing, stop and tell the user to install
  it: `go install github.com/olddognewflex/sift/cmd/sift@latest`, or
  `go install ./cmd/sift` from a Sift checkout, with `~/go/bin` on `PATH`.
- The target is the first non-flag argument, or `.` if there is none. It
  must be inside a Git repository.

## 2. Decide the dirty-tree policy

Unless the arguments include `--dirty`, run `git -C <path> status --porcelain`.
If it prints anything, ask the user (with AskUserQuestion when available)
which policy to use, because Sift refuses a dirty tree by default:

- `include`: audit the working tree, uncommitted changes included
- `head`: audit the last commit only
- cancel

Pass the choice as `--dirty <policy>`.

## 3. Run the audit in the background

Run `sift audit <path> [--harness H] [--model M] [--dirty P]` with the Bash
tool's `run_in_background: true`. An audit takes minutes, and each agent
may run up to its timeout (20 minutes by default), so a foreground call
would hit the Bash timeout. Pass through only the flags the user gave.
Tell the user it has started, which harness it uses (the `--harness`
value, else `harness.name` from `./sift.yaml`, else `claude-code`), and that
it makes live model calls.

Wait for the completion notification. Do not poll or sleep.

Sift reads `./sift.yaml` from the current directory when present; the user
can create one with `sift init`.

## 4. Interpret the exit status

Read the command output. It ends with `Run:`, `Status:`, and `Reports:`.

| Exit | Meaning | What to say |
|---|---|---|
| 0 | completed | Summarize the results |
| 2 | partial: some stages failed | Summarize, and say plainly that results are incomplete; run `sift status --json --run <id>` and name the failed stages |
| 130 | interrupted | Partial results were saved; say so |
| 1 | failed or could not start | Show the error. Common causes: dirty tree, harness not installed or not logged in (`kiro-cli login`), unknown model name |

Never present a partial or failed audit as a clean result.

## 5. Read and summarize the results

Run `sift findings --json --run <id>`. This runs in a later turn than the
skill, so it may need the user's permission; if it is denied, say so and
give the exact command and report directory instead of guessing at the
findings. Each finding has `id`, `title`,
`claim`, `locations` (repo-relative `path`, `start_line`, `end_line`),
`effective_severity`, `effective_confidence`, `remediation`, and
`validation_state`:

- `confirmed`: a defect validated against the source
- `supported_risk`: a real risk, validated
- `needs_investigation`: unresolved; evidence was not conclusive
- `rejected`: the validator refuted it
- `unvalidated`: validation could not run; never treat as accepted

Lead with the outcome in one sentence (for example, "Sift confirmed 2
defects and 1 risk in 4 files"). Then list `confirmed` and
`supported_risk` findings, most severe first, each with its ID, title,
`path:start_line`, severity, and a one-line remediation. Then give one line
each for the counts of `needs_investigation`, `rejected` and `unvalidated`
findings. Finish with the report directory from the `Reports:` line; the
key files are `executive-summary.md`, `findings.md` and
`remediation-plan.md`. Read them with the Read tool if the user wants
detail.

Do not restate rejected findings as problems, and do not add findings of
your own; Sift's value is that every reported finding survived validation.
Offer to fix specific confirmed findings, but only change code if the
user asks.
