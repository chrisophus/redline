#!/usr/bin/env bash
# Sandbox Studio's checks for this repository. Each runs in a slot's worktree as the agent user:
#   build  gofmt on cmd and internal (what `make fmt` and CI check), go vet and a build
#   test   the test suite with the race detector, as CI runs it
# golangci-lint and the gorefactor lint are left to CI: they need tools a studio host may not have,
# and the studio sends a failing CI run back to the agent.
# Go's caches live in the slot, so one slot's build never reads what another slot's agent wrote.
set -euo pipefail
export PATH="/usr/local/go/bin:$PATH"
export GOCACHE="${SANDBOX_SLOT_DIR:?}/gocache" GOMODCACHE="${SANDBOX_SLOT_DIR}/gomod"
case "${1:-}" in
  build)
    bad="$(gofmt -l ./cmd ./internal)"
    [ -z "$bad" ] || { echo "gofmt would change:" >&2; echo "$bad" >&2; exit 1; }
    go vet ./...
    go build ./...
    ;;
  test) exec go test -race ./... ;;
  *) echo "usage: check.sh build|test" >&2; exit 2 ;;
esac
