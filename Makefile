.PHONY: build install install-integrations test race e2e conformance schemas lint clean

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

# Regenerate schemas/*.json from the Go contract types.
schemas:
	UPDATE_SCHEMAS=1 go test ./internal/schema -run TestPublishedSchemasUpToDate -count=1

lint:
	gofmt -l . | (! grep .)
	go vet ./...

clean:
	rm -rf bin
