.PHONY: build test race e2e conformance schemas lint clean

build:
	go build -o bin/sift ./cmd/sift

test:
	go test ./...

race:
	go test -race ./...

# Live audit with the real Claude Code harness (costs money).
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
