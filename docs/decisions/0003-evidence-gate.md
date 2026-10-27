# ADR 0003: Sift's mechanical evidence gate overrides validator acceptance

**Status:** accepted

## Context

Pruv is an LLM. It can be persuaded, can misread, and can accept a finding
whose cited code doesn't exist. "Do not allow unsupported claims to become
confirmed findings" can't rest on an LLM's judgment alone.

## Decision

1. Reviewers must quote cited lines verbatim (`excerpt`). Sift checks every
   source evidence item against the snapshot. Matching ignores indentation
   and blank lines, and allows partial first and last lines. Results:
   - `verified`: the excerpt is within the cited range.
   - `relocated`: the excerpt exists elsewhere in the file. Corrected lines
     are recorded. Excerpts under 20 characters aren't relocated, since they
     would match anywhere.
   - `mismatch`, `missing_file`, `invalid_range`: the evidence contradicts
     the audited revision.
   - `not_verifiable`: execution evidence (agents can't run commands).
2. Pruv sees these check results and must re-read the source itself.
3. A deterministic gate decides the final state:
   - Pruv `reject` → `rejected`. `investigate` → `needs_investigation`.
   - Pruv `accept` → `confirmed` (defect) or `supported_risk` (risk) only
     if ≥1 evidence item is verified or relocated and none contradicts the
     revision, including evidence Pruv added. Otherwise →
     `needs_investigation`, with the reason recorded.
   - Pruv failure → `unvalidated`.
4. Final states are terminal within a run. Original candidates are never
   modified.

## Consequences

- A fabricated citation can't produce a confirmed finding, whatever any
  agent says. The test fixture includes one; the gate must catch it.
- Strict quoting will sometimes block a correct finding whose excerpt was
  reformatted. It lands in `needs_investigation`, not `rejected`, so it
  stays visible.
- The checks prove the cited code exists, not that the reasoning is right.
  The reasoning is Pruv's job.
