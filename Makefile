.PHONY: build install install-integrations test race e2e conformance bench bench-rescore schemas lint clean

build:
	go build -o bin/sift ./cmd/sift

# Install the sift binary into $(go env GOPATH)/bin (or GOBIN).
install:
	go install ./cmd/sift

# Install the /sift-audit skill for Claude Code and the sift-audit agent for
# Kiro CLI (user level). Re-run after updating Sift to refresh them.
install-integrations:
	mkdir -p $(HOME)/.claude/skills $(HOME)/.kiro/agents
	rm -rf $(HOME)/.claude/skills/sift-audit
	cp -R integrations/claude-code/skills/sift-audit $(HOME)/.claude/skills/sift-audit
	cp integrations/kiro/agents/sift-audit.json $(HOME)/.kiro/agents/sift-audit.json

test:
	go test ./...

race:
	go test -race ./...

# Live audit of a fixture repository through a real harness (costs money or
# Kiro credits). Select harnesses with SIFT_E2E_HARNESS, e.g.
# SIFT_E2E_HARNESS=claude-code,kiro make e2e (default: claude-code).
e2e:
	SIFT_E2E=1 go test ./internal/e2e -v -count=1 -timeout 30m

# Live harness conformance suite for every installed adapter: isolation,
# structured output, timeouts, budgets. Costs money (Claude) and Kiro
# credits. See internal/harness/conformance.
conformance:
	SIFT_CONFORMANCE=1 go test ./internal/harness/... -run TestConformance -v -count=1 -timeout 30m

# Live benchmark (docs/benchmark.md): audits every fixture through a real
# harness and scores the findings. Costs money (Claude) or Kiro credits.
# Without BENCH_BUDGET it only prints the cost estimate and spends nothing:
#   make bench                    # estimate only
#   make bench BENCH_BUDGET=26    # run within $26 (the estimate prints this)
# Optional: BENCH_TRIALS=<1-10>, BENCH_FIXTURES=ledger,invoicer, and
# BENCH_UPDATE_BASELINE=1 to rewrite bench/baseline.json. Each is passed
# only when set, so SIFT_BENCH_* values exported in the shell apply to
# every make bench (check for a lingering SIFT_BENCH_UPDATE_BASELINE=1).
# Harness and model: SIFT_BENCH_HARNESS, SIFT_BENCH_MODEL.
bench:
	SIFT_BENCH=1 \
	$(if $(BENCH_BUDGET),SIFT_BENCH_BUDGET_USD='$(BENCH_BUDGET)') \
	$(if $(BENCH_TRIALS),SIFT_BENCH_TRIALS='$(BENCH_TRIALS)') \
	$(if $(BENCH_FIXTURES),SIFT_BENCH_FIXTURES='$(BENCH_FIXTURES)') \
	$(if $(BENCH_UPDATE_BASELINE),SIFT_BENCH_UPDATE_BASELINE='$(BENCH_UPDATE_BASELINE)') \
	go test ./internal/bench -run TestBenchLive -v -count=1 -timeout 6h

# Rescore a benchmark results file (default: the baseline) with the current
# keys and matching rules, offline and free: make bench-rescore RESULTS=<file>
bench-rescore:
	@f='$(or $(RESULTS),bench/baseline.json)'; \
	case "$$f" in /*) ;; *) f="$(CURDIR)/$$f" ;; esac; \
	if [ ! -f "$$f" ]; then echo "bench-rescore: $$f not found; pass RESULTS=<results file>, or record a baseline first (docs/benchmark.md)" >&2; exit 1; fi; \
	SIFT_BENCH_RESCORE="$$f" go test ./internal/bench -run TestBenchRescore -v -count=1

# Regenerate schemas/*.json from the Go contract types.
schemas:
	UPDATE_SCHEMAS=1 go test ./internal/schema -run TestPublishedSchemasUpToDate -count=1

lint:
	gofmt -l . | (! grep .)
	go vet ./...

clean:
	rm -rf bin
