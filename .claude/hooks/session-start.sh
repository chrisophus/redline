#!/bin/bash
# Put what `make check` needs into a fresh Claude Code on the web container.
#
# Everything but the linter is already there: the Go toolchain comes from
# go.mod, and the module cache is warmed here so the first `go test` of a
# session compiles rather than downloads. golangci-lint is the one gap. The
# image ships an older one, `make lint` prefers the pinned install in
# GOPATH/bin over whatever is on PATH, and without this hook every session
# starts by either running the wrong linter or installing the right one by
# hand.
#
# The pinned version is read out of the Makefile rather than written here.
# This is the fourth place that would otherwise carry the number, and
# internal/toolchain exists because three was already too many.
set -euo pipefail

# Local sessions keep their own toolchain. A hook that installed a linter on
# somebody's laptop because they opened Claude Code would be a surprise. The
# check is ahead of the line below so a local session does not read a promise
# of work that is not coming.
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

# Asynchronous, so a session starts while this runs rather than a minute
# after it. The window it opens is real: `make lint` inside that minute finds
# the older golangci-lint the image ships, which refuses to start against
# .golangci.yml's run.go and says so. It is a clear failure rather than a
# quiet one, and running it again a moment later is the fix.
echo '{"async": true, "asyncTimeout": 300000}'

cd "${CLAUDE_PROJECT_DIR:-"$(dirname "$0")/../.."}"

# Downloads the toolchain go.mod declares as well as the modules.
go mod download

want=$(sed -n 's/^GOLANGCI_VERSION := //p' Makefile)
have=$("$(go env GOPATH)/bin/golangci-lint" version --short 2>/dev/null || true)
if [ -z "$want" ]; then
  echo "session-start: the Makefile pins no golangci-lint version, so none was installed" >&2
  exit 0
fi
if [ "v$have" = "$want" ]; then
  echo "session-start: golangci-lint $want is already installed"
  exit 0
fi

# Built from source through the module proxy, with the toolchain the Makefile
# derives from go.mod: a linter built against an older Go than .golangci.yml's
# run.go refuses to start. It takes about a minute, once per container, and
# the session is already running by then.
make lint-install
