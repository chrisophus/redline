# Redline — build, test, and install.
#
# `make install` puts the binary on PATH and the skill where Claude Code and
# Cursor look for it. Both are symlinks into this checkout on purpose: the
# binary and the skill that drives it move together, and a stale copy of
# either is the failure this target exists to prevent — an installed binary
# that predates a packet field the skill relies on reads as a tool bug.

BIN      := redline
# A context provider is a separate program by design, and this one is a
# separate program that happens to live here: it reads a Graphify graph and
# writes an envelope, links no language toolchain, and Redline finds it on
# PATH through .redline.yml like any other provider.
ADAPTER  := redline-graphify-context
# The scout is the provider that costs money: it calls a model to decide what
# the reviewer needs. It ships beside the others because a provider that is
# not on PATH is a context layer that silently does not run, and it does
# nothing at all until a repository names it in .redline.yml.
SCOUT    := redline-scout
BINDIR   ?= $(HOME)/.local/bin
SKILLDIR ?= $(HOME)/.claude/skills
ROOT     := $(abspath $(dir $(firstword $(MAKEFILE_LIST))))
# Every directory under skills/ is a skill: redline drives the binary,
# redline-setup wires a repository's tools into it.
SKILLS   := $(notdir $(wildcard $(ROOT)/skills/*))

# Version stamped into the binary. A tagged checkout gets the tag; anything else
# gets a describe string or "dev", so `redline version` never claims to be a
# release it is not.
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: all build test vet fmt check boundary install install-bin install-skill \
        install-repo uninstall clean lint lint-bin lint-install \
        gorefactor-lint gorefactor-doctor \
        coverage bench mutate mutate-full mutants snapshot release

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/redline
	go build -ldflags "$(LDFLAGS)" -o $(ADAPTER) ./cmd/redline-graphify-context
	go build -ldflags "$(LDFLAGS)" -o $(SCOUT) ./cmd/redline-scout

test:
	go test ./...

vet:
	go vet ./...

# golangci-lint v2 keeps formatters in their own section and this repo
# declares none, so nothing else checks this. Its own target so `make check`
# and CI agree about what "formatted" means.
fmt:
	@unformatted=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$unformatted" ]; then \
	  echo "gofmt -w these files:"; echo "$$unformatted"; exit 1; \
	fi

# The version CI gates on. Named here because a local run that disagrees with
# the gate is worse than no local run: it either passes what CI will fail or
# fails what CI will pass. internal/toolchain's test holds this and
# .github/workflows/ci.yml to the same number.
GOLANGCI_VERSION := v2.13.2

# lint-bin prints the golangci-lint this repository would run: the pinned one
# `make lint-install` puts in GOPATH/bin when it is there, and whatever is
# first on PATH otherwise.
#
# The pinned one wins on purpose. A container image or a package manager that
# ships its own golangci-lint shadows the install, and the older binary either
# checks less than CI does or, built against an older Go than .golangci.yml's
# `run.go`, does not lint at all: it bails out during type-checking with a
# message about Go versions that says nothing about how to fix it. An image
# shipping v2.5.0 is what surfaced this: the lint target printed a note about
# the pin and then linted nothing, and the note read as advice rather than as
# the reason there was no output.
lint-bin:
	@gopath=$$(go env GOPATH); \
	pinned="$$gopath/bin/golangci-lint"; \
	if [ -x "$$pinned" ] && [ "v$$($$pinned version --short 2>/dev/null)" = "$(GOLANGCI_VERSION)" ]; then \
	  echo "$$pinned"; \
	elif command -v golangci-lint >/dev/null 2>&1; then \
	  command -v golangci-lint; \
	fi

# Install the version CI gates on, from source through the module proxy, so
# this needs nothing but the Go toolchain already required to build Redline.
# It lands in GOPATH/bin, which is where lint-bin looks first.
lint-install:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@echo "installed $$(go env GOPATH)/bin/golangci-lint"

# golangci-lint reads .golangci.yml. Not installed is not an error here: a
# missing tool darks this target the same way an unconfigured repo darks
# redline's own lint-delta pane (README's "Lint" section) rather than
# failing the build for a dev who hasn't installed it. CI installs it and
# so gets the real gate.
#
# A version that is not the pinned one is a note rather than a refusal, for
# the same reason, but it is said out loud, and it names the target that
# fixes it rather than the command behind it.
lint:
	@bin=$$($(MAKE) -s lint-bin); \
	if [ -z "$$bin" ]; then \
	  echo "skip: golangci-lint not installed (make lint-install)"; \
	  exit 0; \
	fi; \
	have=v$$($$bin version --short 2>/dev/null); \
	if [ "$$have" != "$(GOLANGCI_VERSION)" ]; then \
	  echo "note: golangci-lint $$have is what this would run and CI gates on $(GOLANGCI_VERSION)"; \
	  echo "      make lint-install"; \
	fi; \
	echo "$$bin run ./..."; \
	$$bin run ./...

# gorefactor (github.com/chrisophus/gorefactor) is a separate project's
# structural linter; it is not a dependency of Redline and self-skips when
# absent, same reasoning as the golangci-lint target above.
#
# The skip and the run are one recipe line because make gives each line its
# own shell: an `exit 0` on the first line ends that shell and make starts the
# next one, so the skip printed its message and then ran the tool anyway. On a
# checkout without gorefactor that was `make check` failing with "No such file
# or directory" after saying it would skip.
gorefactor-lint:
	@if ! command -v gorefactor >/dev/null 2>&1; then \
	  echo "skip: gorefactor not on PATH"; \
	  exit 0; \
	fi; \
	echo "gorefactor lint ."; \
	gorefactor lint .

gorefactor-doctor:
	@if ! command -v gorefactor >/dev/null 2>&1; then \
	  echo "skip: gorefactor not on PATH"; \
	  exit 0; \
	fi; \
	echo "gorefactor doctor"; \
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

# gomutants (github.com/szhekpisov/gomutants) writes mutants.json, the report
# redline's mutation section reads. Diff-scoped against origin/main so it stays
# a per-change check, not a whole-repo batch. Self-skips if gomutants is absent.
GOMUTANTS_BASE ?= origin/main
mutants:
	@if ! command -v gomutants >/dev/null 2>&1; then \
	  echo "skip: gomutants not on PATH (go install github.com/szhekpisov/gomutants@latest)"; \
	  exit 0; \
	fi
	gomutants --changed-since $(GOMUTANTS_BASE) -o mutants.json ./...

# Redline is language-agnostic, and everything language-specific lives behind
# the provider interface. That claim is worth what its enforcement is worth,
# so it has a target of its own and runs in CI as its own step.
boundary:
	go test ./internal/boundary/ -v

check: fmt vet test lint gorefactor-lint boundary

# install is for this machine: the skill becomes available in every repo.
install: install-bin install-skill
	@echo
	@echo "Installed. Run /redline-setup once per repository, then /redline."
	@case ":$$PATH:" in \
	  *":$(BINDIR):"*) ;; \
	  *) echo "warning: $(BINDIR) is not on PATH; add it to your shell profile" ;; \
	esac

install-bin: build
	@mkdir -p $(BINDIR)
	@ln -sfn $(ROOT)/$(BIN) $(BINDIR)/$(BIN)
	@echo "$(BINDIR)/$(BIN) -> $(ROOT)/$(BIN)"
	@ln -sfn $(ROOT)/$(ADAPTER) $(BINDIR)/$(ADAPTER)
	@echo "$(BINDIR)/$(ADAPTER) -> $(ROOT)/$(ADAPTER)"
	@ln -sfn $(ROOT)/$(SCOUT) $(BINDIR)/$(SCOUT)
	@echo "$(BINDIR)/$(SCOUT) -> $(ROOT)/$(SCOUT)"

# The skill directory is symlinked, not copied, so editing skills/redline/
# in this checkout is immediately what the agent reads. A copy drifts, and
# a hardlink is worse: git replaces files on checkout rather than writing
# through them, so the link breaks silently and the drift is invisible.
install-skill:
	@mkdir -p $(SKILLDIR)
	@for s in $(SKILLS); do \
	  if [ -e "$(SKILLDIR)/$$s" ] && [ ! -L "$(SKILLDIR)/$$s" ]; then \
	    if diff -q "$(SKILLDIR)/$$s/SKILL.md" "$(ROOT)/skills/$$s/SKILL.md" >/dev/null 2>&1; then \
	      rm -rf "$(SKILLDIR)/$$s"; \
	    else \
	      mv "$(SKILLDIR)/$$s" "$(SKILLDIR)/$$s.backup.$$(date +%s)"; \
	      echo "existing skill $$s differed; moved it aside"; \
	    fi; \
	  fi; \
	  ln -sfn "$(ROOT)/skills/$$s" "$(SKILLDIR)/$$s"; \
	  echo "$(SKILLDIR)/$$s -> $(ROOT)/skills/$$s"; \
	done

# install-repo commits the skill into another repository, for teammates who
# do not have this checkout. A project skill overrides the personal one, so
# the repo's copy is what everyone working in it gets. This one is a copy by
# necessity: it has to survive without this checkout on disk.
install-repo:
	@test -n "$(REPO)" || { echo "usage: make install-repo REPO=/path/to/repo"; exit 1; }
	@test -d "$(REPO)" || { echo "no such directory: $(REPO)"; exit 1; }
	@for s in $(SKILLS); do \
	  mkdir -p "$(REPO)/.claude/skills/$$s"; \
	  cp "$(ROOT)/skills/$$s/SKILL.md" "$(REPO)/.claude/skills/$$s/SKILL.md"; \
	  echo "$(REPO)/.claude/skills/$$s/SKILL.md (copy: commit it)"; \
	done
	@echo "note: teammates still need the redline binary on PATH."

uninstall:
	@rm -f $(BINDIR)/$(BIN) $(BINDIR)/$(ADAPTER) $(BINDIR)/$(SCOUT)
	@for s in $(SKILLS); do \
	  if [ -L "$(SKILLDIR)/$$s" ]; then rm -f "$(SKILLDIR)/$$s"; fi; \
	done
	@echo "Removed $(BINDIR)/$(BIN) and the skills under $(SKILLDIR)."

clean:
	rm -f $(BIN) $(ADAPTER) $(SCOUT)

# goreleaser cuts the cross-platform release. snapshot builds locally without a
# tag or publishing, to check the config; release runs in CI on a pushed tag.
snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean
