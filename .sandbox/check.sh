#!/usr/bin/env bash
# The studio's checks for this project (.sandbox/sandbox.yaml): build, lint, test, the same gates
# as CI (.github/workflows/ci.yml). Runs in the slot's worktree as the agent user, so Go's caches
# live in the slot directory and tools are run at CI's pinned versions with `go run`.
set -euo pipefail
export PATH="/usr/local/go/bin:$PATH"
export GOCACHE="${SANDBOX_SLOT_DIR:?}/gocache" GOMODCACHE="${SANDBOX_SLOT_DIR}/gomod"
case "${1:-}" in
  build)
    bad="$(gofmt -l .)"
    [ -z "$bad" ] || { echo "gofmt would change:" >&2; echo "$bad" >&2; exit 1; }
    go vet ./...
    go build ./...
    ;;
  lint)
    # The version is tied to the host's Go. `go run` builds the linter with the Go on this host, and
    # a golangci-lint older than that Go cannot read its export data, so every package fails to
    # typecheck (v2.13.2 on Go 1.27 did). When the host's Go moves up, bump this pin with it, and
    # keep it equal to GOLANGCI_VERSION in the Makefile, ci.yml and bash_allow in sandbox.yaml.
    go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
    go test ./internal/boundary/
    ;;
  test) exec go test -race ./... ;;
  *) echo "usage: check.sh build|lint|test" >&2; exit 2 ;;
esac
