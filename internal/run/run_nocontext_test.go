package run_test

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/run"
)

// --no-context gathers nothing beyond the diff and records that as a choice.
// The provider here is missing, so a run that tried it reports it absent; the
// run told to skip must not try it, and must not read as one that broke.
func TestSkipContextRunsNoProvider(t *testing.T) {
	r := newRepo(t)
	r.write(".redline.yml", "context:\n  - name: absentprovider\n    command: redline-no-such-provider\n    scope: [\"**/*.go\"]\n")
	r.write("a.go", "package a\n\nfunc A() int { return 1 }\n")
	r.write("b.go", "package a\n\nfunc B() int { return 2 }\n")
	r.commit("init")
	r.write("a.go", "package a\n\nfunc A() int { return 3 }\n")

	with := r.run(run.Options{Base: "main", SkipLint: true})
	if len(with.ContextAbsent) == 0 {
		t.Fatal("control: the missing provider was not reported absent, so the skip below proves nothing")
	}

	without := r.run(run.Options{Base: "main", SkipLint: true, SkipContext: true})
	if len(without.Envelopes) != 0 || len(without.ContextAbsent) != 0 {
		t.Errorf("--no-context still gathered context: %d envelope(s), absent %v",
			len(without.Envelopes), without.ContextAbsent)
	}
	var said bool
	for _, u := range without.Report.Unknowns {
		if strings.Contains(u.Message, "--no-context") {
			said = true
		}
		if strings.Contains(u.Message, "did not run") {
			t.Errorf("a skipped provider reads as a broken one: %s", u.Message)
		}
	}
	if !said {
		t.Error("the report does not say context was skipped on request")
	}
}
