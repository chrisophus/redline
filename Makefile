# Redline — build, test, and install.
#
# `make install` puts the binary on PATH and the skill where Claude Code and
# Cursor look for it. Both are symlinks into this checkout on purpose: the
# binary and the skill that drives it move together, and a stale copy of
# either is the failure this target exists to prevent — an installed binary
# that predates a packet field the skill relies on reads as a tool bug.

BIN      := redline
BINDIR   ?= $(HOME)/.local/bin
SKILLDIR ?= $(HOME)/.claude/skills
ROOT     := $(abspath $(dir $(firstword $(MAKEFILE_LIST))))

.PHONY: all build test vet check install install-bin install-skill \
        install-repo uninstall clean lint gorefactor-lint gorefactor-doctor \
        coverage bench mutate mutate-full

all: build

build:
	go build -o $(BIN) ./cmd/redline

test:
	go test ./...

vet:
	go vet ./...

# golangci-lint reads .golangci.yml. Not installed is not an error here: a
# missing tool darks this target the same way an unconfigured repo darks
# redline's own lint-delta pane (README's "Lint" section) rather than
# failing the build for a dev who hasn't installed it. CI installs it and
# so gets the real gate.
lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
	  echo "skip: golangci-lint not on PATH (go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)"; \
	  exit 0; \
	fi
	golangci-lint run ./...

# gorefactor (github.com/chrisophus/gorefactor) is a separate project's
# structural linter; it is not a dependency of Redline and self-skips when
# absent, same reasoning as the golangci-lint target above.
gorefactor-lint:
	@if ! command -v gorefactor >/dev/null 2>&1; then \
	  echo "skip: gorefactor not on PATH"; \
	  exit 0; \
	fi
	gorefactor lint .

gorefactor-doctor:
	@if ! command -v gorefactor >/dev/null 2>&1; then \
	  echo "skip: gorefactor not on PATH"; \
	  exit 0; \
	fi
	gorefactor doctor

# The coverage profile this repo's own diff-coverage pane (internal/cover)
# reads. `redline run` on this repository is only as honest about its own
# tests as this file is fresh — regenerate it before reviewing a change here.
coverage:
	go test ./... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out | tail -1

bench:
	go test ./... -bench=. -benchtime=10x -run='^$$'

# Mutation testing via gremlins (github.com/go-gremlins/gremlins), scoped to
# lines the working tree changed against origin/main — the same diff-first
# principle as the rest of Redline: a full-module mutation run is a
# multi-minute batch job, not a thing to run on every commit. Advisory only
# (no --threshold-*): a killed/lived count with nothing to compare against
# yet is not something to gate on.
mutate:
	@if ! command -v gremlins >/dev/null 2>&1; then \
	  echo "skip: gremlins not on PATH (go install github.com/go-gremlins/gremlins/cmd/gremlins@latest)"; \
	  exit 0; \
	fi
	gremlins unleash . --diff=origin/main --timeout-coefficient=10

# The unscoped run: every mutant in every package. Slow; run by hand, not in
# a pre-push loop.
mutate-full:
	@if ! command -v gremlins >/dev/null 2>&1; then \
	  echo "skip: gremlins not on PATH"; \
	  exit 0; \
	fi
	gremlins unleash ./... --timeout-coefficient=10

check: vet test lint gorefactor-lint

# install is for this machine: the skill becomes available in every repo.
install: install-bin install-skill
	@echo
	@echo "Installed. Run /redline in any repository."
	@case ":$$PATH:" in \
	  *":$(BINDIR):"*) ;; \
	  *) echo "warning: $(BINDIR) is not on PATH; add it to your shell profile" ;; \
	esac

install-bin: build
	@mkdir -p $(BINDIR)
	@ln -sfn $(ROOT)/$(BIN) $(BINDIR)/$(BIN)
	@echo "$(BINDIR)/$(BIN) -> $(ROOT)/$(BIN)"

# The skill directory is symlinked, not copied, so editing skills/redline/
# in this checkout is immediately what the agent reads. A copy drifts, and
# a hardlink is worse: git replaces files on checkout rather than writing
# through them, so the link breaks silently and the drift is invisible.
install-skill:
	@mkdir -p $(SKILLDIR)
	@if [ -e "$(SKILLDIR)/redline" ] && [ ! -L "$(SKILLDIR)/redline" ]; then \
	  if diff -q "$(SKILLDIR)/redline/SKILL.md" "$(ROOT)/skills/redline/SKILL.md" >/dev/null 2>&1; then \
	    rm -rf "$(SKILLDIR)/redline"; \
	  else \
	    mv "$(SKILLDIR)/redline" "$(SKILLDIR)/redline.backup.$$(date +%s)"; \
	    echo "existing skill differed; moved it aside"; \
	  fi; \
	fi
	@ln -sfn $(ROOT)/skills/redline $(SKILLDIR)/redline
	@echo "$(SKILLDIR)/redline -> $(ROOT)/skills/redline"

# install-repo commits the skill into another repository, for teammates who
# do not have this checkout. A project skill overrides the personal one, so
# the repo's copy is what everyone working in it gets. This one is a copy by
# necessity: it has to survive without this checkout on disk.
install-repo:
	@test -n "$(REPO)" || { echo "usage: make install-repo REPO=/path/to/repo"; exit 1; }
	@test -d "$(REPO)" || { echo "no such directory: $(REPO)"; exit 1; }
	@mkdir -p "$(REPO)/.claude/skills/redline"
	@cp $(ROOT)/skills/redline/SKILL.md "$(REPO)/.claude/skills/redline/SKILL.md"
	@echo "$(REPO)/.claude/skills/redline/SKILL.md (copy — commit it)"
	@echo "note: teammates still need the redline binary on PATH."

uninstall:
	@rm -f $(BINDIR)/$(BIN)
	@if [ -L "$(SKILLDIR)/redline" ]; then rm -f "$(SKILLDIR)/redline"; fi
	@echo "Removed $(BINDIR)/$(BIN) and $(SKILLDIR)/redline."

clean:
	rm -f $(BIN)
