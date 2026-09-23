package run_test

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/run"
)

// What a run prints while it works. A run used to print that it was observing
// and then the report's address, with the panes and the providers between the
// two silent; a provider that was not on PATH was on the report and nowhere
// else, which `review --run` never prints.
func TestARunSaysWhatItRunsAndWhatDidNotRun(t *testing.T) {
	r := providerRepo(t)
	r.write("internal/a/a.go", "package a\n\nfunc A() {}\n")

	var said, verbose []string
	r.run(run.Options{
		Base:     "main",
		Progress: func(s string) { said = append(said, s) },
		Debug:    func(s string) { verbose = append(verbose, s) },
	})

	want := []string{
		"running ",
		"gofake: resolving context for 1 file(s)",
		"gofake did not run: redline-test-no-such-provider is configured for this repository but not on PATH",
		"observed 1 file(s)",
	}
	for _, w := range want {
		if !contains(said, w) {
			t.Errorf("a run must say %q, said: %q", w, said)
		}
	}
	for _, line := range said {
		if strings.HasPrefix(line, "running ") && !strings.Contains(line, "pane(s) over 1 file(s): ") {
			t.Errorf("the panes line must name what runs over what: %q", line)
		}
	}
	// --verbose adds the panes one by one and the provider's command line,
	// which is what a person told it did not run needs to run it by hand.
	for _, w := range []string{"observing 1 file(s)", "gofake: running redline-test-no-such-provider"} {
		if !contains(verbose, w) {
			t.Errorf("--verbose must say %q, said: %q", w, verbose)
		}
	}
	for _, line := range verbose {
		if contains(said, line) {
			t.Errorf("a verbose line was also printed as progress: %q", line)
		}
	}
}

// Nil is the default for both, and the library must stay quiet on it.
func TestARunWithNowhereToReportStillRuns(t *testing.T) {
	r := providerRepo(t)
	r.write("internal/a/a.go", "package a\n\nfunc A() {}\n")
	if res := r.run(run.Options{Base: "main"}); len(res.Report.Scope) != 1 {
		t.Errorf("scope = %v, want the one changed file", res.Report.Scope)
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}
