// Package toolchain holds the checks that keep the gate a developer runs and
// the gate CI runs from drifting apart.
//
// Several files have to agree about two numbers, and nothing but a comment
// said so. .golangci.yml's `run.go` must not be ahead of the Go the linter
// binary was built with, which in practice means it tracks go.mod; and the
// local install and the CI step have to install the same golangci-lint,
// because a lint run that disagrees with the gate is worse than no lint run.
//
// Both are cheap to state and neither fails loudly on its own. A `run.go`
// ahead of the binary does not lint at all, it bails out during type-checking
// with a message about Go versions, and a local binary older than the pin
// quietly checks less than CI will.
//
// The version lives in the Makefile and everything else reads it from there,
// the session hook that installs it in a fresh container included. These
// checks are also what stops the next place that needs it from writing it
// down again.
package toolchain

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// read is a repository file, read relative to this package.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile("../../" + path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// The linter type-checks the tree, so it needs to be told the language
// version the tree is written in. Behind go.mod it rejects code that
// compiles; ahead of the binary's own Go it refuses to start.
func TestTheLinterIsToldTheGoVersionGoModDeclares(t *testing.T) {
	mod := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindStringSubmatch(read(t, "go.mod"))
	if mod == nil {
		t.Fatal("go.mod has no go directive")
	}
	lint := regexp.MustCompile(`(?m)^\s+go: "([^"]+)"`).FindStringSubmatch(read(t, ".golangci.yml"))
	if lint == nil {
		t.Fatal(".golangci.yml sets no run.go, so the linter guesses the language version")
	}
	if lint[1] != mod[1] {
		t.Errorf(".golangci.yml runs as go %s against a go.mod of %s", lint[1], mod[1])
	}
	for _, line := range regexp.MustCompile(`(?m)^\s+go-version: "([^"]+)"`).FindAllStringSubmatch(read(t, ".github/workflows/ci.yml"), -1) {
		if line[1] != mod[1] {
			t.Errorf("ci.yml builds on go %s against a go.mod of %s", line[1], mod[1])
		}
	}
	// The toolchain directive names the release everything local builds with,
	// `make lint-install` included. A release from another line would build
	// and lint with a Go nobody else here is using, which is the drift these
	// checks are about even though the go command would allow it.
	if tc := regexp.MustCompile(`(?m)^toolchain go(\d+\.\d+)`).FindStringSubmatch(read(t, "go.mod")); tc != nil {
		if tc[1] != mod[1] {
			t.Errorf("go.mod builds with the go %s toolchain against a go directive of %s", tc[1], mod[1])
		}
	}
}

// The Makefile names the version so a developer can install it; CI names it so
// the gate is reproducible. They are the same gate or the local run is worth
// nothing.
func TestMakeAndCIInstallTheSameGolangciLint(t *testing.T) {
	mk := regexp.MustCompile(`(?m)^GOLANGCI_VERSION := (\S+)`).FindStringSubmatch(read(t, "Makefile"))
	if mk == nil {
		t.Fatal("the Makefile pins no golangci-lint version")
	}
	ci := read(t, ".github/workflows/ci.yml")
	if !strings.Contains(ci, "golangci/golangci-lint-action") {
		t.Skip("CI no longer runs golangci-lint, so there is nothing to agree with")
	}
	ver := regexp.MustCompile(`(?m)golangci-lint-action@[^\n]*\n(?:\s+[^\n]*\n)*?\s+version: (\S+)`).FindStringSubmatch(ci)
	if ver == nil {
		t.Fatal("the CI step pins no golangci-lint version, so it installs whatever is newest")
	}
	if ver[1] != mk[1] {
		t.Errorf("make lint installs %s and CI gates on %s", mk[1], ver[1])
	}
}

// A second place naming the version is a second place to forget it. The
// install target exists so nobody pastes a version by hand, so it has to take
// the pin rather than restate it.
func TestTheInstallTargetTakesThePinnedVersion(t *testing.T) {
	mk := read(t, "Makefile")
	if !strings.Contains(mk, "golangci-lint@$(GOLANGCI_VERSION)") {
		t.Error("nothing in the Makefile installs the pinned golangci-lint")
	}
	if named := regexp.MustCompile(`golangci-lint@v[0-9][^\s]*`).FindString(mk); named != "" {
		t.Errorf("the Makefile names a golangci-lint version by hand (%s); use $(GOLANGCI_VERSION)", named)
	}
	// The same rule for the Go it is built with. A linter built against an
	// older Go than .golangci.yml's run.go refuses to start, so the install
	// has to follow go.mod rather than a number somebody typed once.
	if !strings.Contains(mk, "GOTOOLCHAIN=$(GO_TOOLCHAIN)") {
		t.Error("the install target does not build the linter with the Go go.mod declares")
	}
	if named := regexp.MustCompile(`GOTOOLCHAIN=go[0-9][^\s]*`).FindString(mk); named != "" {
		t.Errorf("the Makefile names a Go toolchain by hand (%s); derive it from go.mod", named)
	}
}

// The session hook installs the linter into a fresh Claude Code on the web
// container, which makes it a fourth file that could carry the version. It
// reads the pin out of the Makefile instead, and this is what says so.
func TestTheSessionHookReadsThePinRatherThanRepeatingIt(t *testing.T) {
	const path = ".claude/hooks/session-start.sh"
	data, err := os.ReadFile("../../" + path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("no session hook in this checkout, so there is nothing to keep in step")
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	hook := string(data)
	if !strings.Contains(hook, "GOLANGCI_VERSION") {
		t.Errorf("%s does not read the pin from the Makefile", path)
	}
	if named := regexp.MustCompile(`v2\.\d+\.\d+`).FindString(hook); named != "" {
		t.Errorf("%s names golangci-lint %s by hand; read GOLANGCI_VERSION from the Makefile", path, named)
	}
}
