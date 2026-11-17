#!/usr/bin/env bash
# The studio's checks for this project (.sandbox/sandbox.yaml): build, lint, test, the same gates
# as CI (.github/workflows/ci.yml). Runs in the slot's worktree as the agent user, so Go's caches
# live in the slot directory and tools are run at CI's pinned versions with `go run`.
set -euo pipefail
export PATH="/usr/local/go/bin:$PATH"
export GOCACHE="${SANDBOX_SLOT_DIR:?}/gocache" GOMODCACHE="${SANDBOX_SLOT_DIR}/gomod"
# Build, lint and test with the Go that go.mod names, as CI does, whatever Go the host has. The
# host's Go can be newer than the linter understands: golangci-lint v2.15.0 is built for Go 1.26 and
# cannot decode the export data of Go 1.27, so every package failed to typecheck. `go` downloads
# this toolchain on first use. The toolchain line wins over the go line, as in the Makefile.
GOTOOLCHAIN="$(awk '/^toolchain /{t=$2} /^go /{g=$2} END{if (t != "") print t; else {if (split(g, p, ".") == 2) g = g ".0"; print "go" g}}' go.mod)"
export GOTOOLCHAIN
case "${1:-}" in
  build)
    bad="$(gofmt -l .)"
    [ -z "$bad" ] || { echo "gofmt would change:" >&2; echo "$bad" >&2; exit 1; }
    go vet ./...
    go build ./...
    ;;
  lint)
    # Keep this equal to GOLANGCI_VERSION in the Makefile, ci.yml and bash_allow in sandbox.yaml.
    # The linter is built with the Go from go.mod (see GOTOOLCHAIN above), not the host's, so it can
    # read the export data of the standard library it typechecks against.
    go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
    go test ./internal/boundary/
    ;;
  test) exec go test -race ./... ;;
  *) echo "usage: check.sh build|lint|test" >&2; exit 2 ;;
esac
