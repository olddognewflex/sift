# Sift

**Engineering clarity, backed by evidence.**

Sift is a multi-agent software engineering audit tool that evaluates architecture, correctness, performance, security, and maintainability through evidence-driven analysis and pragmatic recommendations.

Rather than simply identifying potential problems, Sift challenges its own findings, validates supporting evidence, and prioritizes improvements based on actual engineering impact.

**Find what matters. Prove what doesn't work. Preserve what does.**

## Why Sift?

Traditional static analysis tools excel at identifying specific classes of defects. AI-assisted code reviews can reason about broader architectural and implementation concerns, but often produce speculative findings, unnecessary refactoring recommendations, and advice disconnected from a project's actual requirements.

Sift is designed to bridge that gap.

It approaches software reviews as a coordinated engineering investigation, using specialized agents to examine different aspects of a system and an independent validation stage to challenge their conclusions.

The goal is not architectural perfection.

**The goal is the simplest robust architecture that meets the system's real requirements.**

## Core Principles

### Evidence Over Opinion

Every actionable finding must be supported by traceable evidence, such as source code, execution paths, test results, or performance measurements.

Speculation is explicitly distinguished from verified defects.

### Pragmatism Over Purism

Architectural recommendations must justify their complexity.

Sift does not recommend microservices, design patterns, abstractions, or rewrites simply because they represent fashionable engineering practices.

### Independent Validation

Findings are challenged before being accepted.

The validation process evaluates evidence, investigates counterexamples, and rejects unsupported conclusions.

### Read-Only by Default

Auditing should not modify the project being evaluated.

Sift separates investigation from remediation and requires explicit authorization for potentially destructive operations.

### Context Matters

Engineering decisions are evaluated against actual project requirements, scale, operational constraints, and business objectives.

A solution appropriate for a small application may be inappropriate for a distributed enterprise platform, and vice versa.

### Preserve What Works

A good audit identifies sound engineering decisions as well as problems.

Not every review needs to produce a refactoring backlog.

## Architecture

Sift uses a staged, multi-agent workflow.

```text
                 ┌─────────────────────┐
                 │  Sift Orchestrator  │
                 └──────────┬──────────┘
                            │
                 ┌──────────▼──────────┐
                 │   Discovery Agent   │
                 └──────────┬──────────┘
                            │
                 ┌──────────▼──────────┐
                 │  Risk & Scope Plan  │
                 └──────────┬──────────┘
                            │
             ┌──────────────┼──────────────┐
             │              │              │
      ┌──────▼──────┐ ┌─────▼──────┐ ┌─────▼──────┐
      │Architecture │ │Correctness │ │Performance │
      │   Review    │ │   Review   │ │   Review   │
      └──────┬──────┘ └─────┬──────┘ └─────┬──────┘
             │              │              │
             └──────────────┼──────────────┘
                            │
                 ┌──────────▼──────────┐
                 │        Pruv         │
                 │ Evidence Validator  │
                 └──────────┬──────────┘
                            │
                 ┌──────────▼──────────┐
                 │  Report Synthesizer │
                 └──────────┬──────────┘
                            │
                 ┌──────────▼──────────┐
                 │  Findings & Roadmap │
                 └─────────────────────┘
```

Additional specialist reviewers may include security, reliability, engineering standards, and testing.

### Orchestrator

Coordinates execution, manages task dependencies, tracks audit state, enforces execution policies, and collects artifacts.

The orchestrator does not independently determine whether engineering findings are valid.

### Discovery Agent

Builds an understanding of the project before evaluation begins.

Responsibilities include identifying:

- Project purpose and primary workflows
- Technology stack and dependencies
- Architectural boundaries
- Data models and persistence
- External integrations
- Deployment and infrastructure
- Testing and development conventions
- Critical execution paths
- Unknown requirements and constraints

### Specialist Reviewers

Independent agents examine the system through focused engineering perspectives.

| Agent | Responsibility |
|---|---|
| Architecture | Design, boundaries, coupling, and architectural fitness |
| Correctness | Logic, contracts, concurrency, and edge cases |
| Performance | Algorithms, queries, latency, and resource efficiency |
| Security | Trust boundaries, authorization, and vulnerabilities |
| Reliability | Failure handling, recovery, and operational resilience |
| Standards | Idioms, conventions, and maintainability |
| Testing | Test quality, critical coverage, and verification gaps |

Specialists generate candidate findings, not final conclusions.

### Pruv — Evidence Validator

Pruv is Sift's adversarial validation component.

Its responsibility is to challenge findings and determine whether they are adequately supported.

It investigates:

- Source evidence
- Reachability of failure conditions
- Existing safeguards
- Contradictory evidence
- Reproduction and verification steps
- Realistic impact
- Proposed remediation trade-offs

Findings may be accepted, rejected, or returned for additional investigation.

### Report Synthesizer

Consolidates validated findings into an actionable engineering assessment.

Reports distinguish confirmed defects, supported risks, unresolved hypotheses, and architectural improvement opportunities.

## Audit Workflow

1. **Initialize** — Capture repository identity, revision, configuration, and execution policy.
2. **Discover** — Map the system and identify important execution paths.
3. **Plan** — Prioritize review areas based on complexity, risk, and business impact.
4. **Review** — Execute specialized engineering investigations.
5. **Validate** — Challenge findings and verify supporting evidence.
6. **Report** — Produce an engineering scorecard and prioritized remediation plan.
7. **Track** — Optionally convert approved recommendations into development tasks.

Each stage produces structured artifacts that can be inspected, reused, and independently validated.

## Findings

Sift findings are structured, traceable, and tied to a specific source revision.

A finding includes:

- Unique identifier and category
- Description and affected locations
- Severity and confidence
- Supporting evidence
- Triggering conditions
- Expected impact
- Recommended remediation
- Alternatives and trade-offs
- Validation status
- Verification criteria

Severity and confidence are evaluated independently.

A potentially serious issue with insufficient evidence must not be presented as a confirmed defect.

## Reporting

Sift is designed to generate three primary reports.

**Executive Summary** — Engineering health, key strengths, significant risks, and recommended priorities.

**Architecture Audit** — Detailed technical findings, supporting evidence, system context, review coverage, and engineering scorecards.

**Remediation Plan** — Prioritized changes with acceptance criteria, verification steps, and implementation considerations.

Machine-readable JSON artifacts accompany Markdown reports to support integrations and automation.

## CLI

```bash
make build                      # builds bin/sift (Go 1.27+)
sift init                       # writes a documented sift.yaml with defaults
sift audit .                    # runs a full audit of the Git repository
sift audit . --dirty=include    # audits uncommitted changes too (default: refuse)
sift status [--run ID] [--json] # shows run state, tasks, cost, warnings
sift findings [--state confirmed] [--json]
sift report [--regenerate]      # prints report paths, or rebuilds them from artifacts
```

`sift audit` exits with 0 when the audit completed, 2 when results are partial, 130 when interrupted, and 1 when it failed.
Runs are stored in `~/.sift/runs/<run-id>/`, which is never inside the audited repository.
Each run directory holds the manifest, the read-only source snapshot, every agent prompt and its raw output, the validated artifacts, and the reports.

`sift verify` is planned but not implemented yet.

## Current Status

The MVP (Phase 1) is implemented:

- Discovery, Architecture, and Correctness agents
- Pruv validation
- A mechanical evidence gate
- Deterministic Markdown reports
- A Claude Code harness adapter

What's built and what isn't: [docs/architecture.md](docs/architecture.md).
Key decisions: [docs/decisions/](docs/decisions/).
Artifact JSON Schemas: [schemas/](schemas/).

Requirements: Git, and Claude Code 2.1.311 or later on `PATH`, signed in.

Agents run read-only against a snapshot. Claude Code enforces this, but it is not an operating-system sandbox. See [ADR 0002](docs/decisions/0002-claude-code-harness.md) for exactly what is and isn't guaranteed.

## Technology Direction

Sift is intended to use:

- **Go** for the CLI and orchestration engine
- **JSON Schema** for artifact contracts
- **Markdown** for agent definitions and human-readable reports
- **Filesystem-based persistence** for audit runs and state
- **Pluggable harness adapters** for AI agent execution

Sift should remain independent of any specific AI provider or coding harness.

Potential integrations include Claude Code, Codex, and other compatible agent execution environments.

## Development Roadmap

### Phase 1 — Minimum Viable Audit

- CLI foundation
- Repository discovery
- Architecture reviewer
- Correctness reviewer
- Evidence validator (Pruv)
- Structured findings
- Markdown reports
