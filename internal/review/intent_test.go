package review

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/target"
)

// The first thing the prompt asks for is a change that does not do what its
// author says it does. That needs what the author said: the pull request's
// body and the commit bodies, which were captured for this and never sent.
func TestThePromptCarriesWhatTheAuthorSaidTheChangeDoes(t *testing.T) {
	in := Input{Report: &findings.Report{}, Change: &change.Set{
		Target: &target.Target{PR: &target.PullRequest{
			Number: 7, Title: "Stop double-charging on retry",
			Body: "The retry path called charge() twice.\n\nThis adds an idempotency key.",
		}},
		Commits: []gitx.Commit{{
			Subject: "Add an idempotency key to charge",
			Body:    "Without it a retried request charged twice.",
		}},
		Files: []change.File{{Path: "pay.go", Status: "modified", Diff: "--- pay.go\n+key := id\n"}},
	}}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Pull request #7: Stop double-charging on retry",
		"This adds an idempotency key.",
		"Add an idempotency key to charge",
		"Without it a retried request charged twice.",
		"it is not evidence about the code",
	} {
		if !strings.Contains(got.Prompt, want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}
}

// A body that runs to pages is pasted output or a template. The part that
// says what the change is for is the first part, and a cut is said out loud
// so a truncated description is not read as a short one.
func TestALongDescriptionIsCutAndSaysSo(t *testing.T) {
	body := strings.Repeat("what the change is for\n", 400)
	in := Input{Report: &findings.Report{}, Change: &change.Set{
		Target: &target.Target{PR: &target.PullRequest{Number: 1, Title: "t", Body: body}},
		Files:  []change.File{{Path: "a.go", Status: "modified", Diff: "--- a.go\n+x\n"}},
	}}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Prompt, "cut at 2000 characters") {
		t.Error("a cut description does not say it was cut")
	}
	if strings.Count(got.Prompt, "what the change is for") > 100 {
		t.Error("the whole description went in")
	}
}

// A working-tree change has no author account at all, and the section says
// nothing rather than announcing an empty one.
func TestNoIntentMeansNoIntentSection(t *testing.T) {
	in := Input{Report: &findings.Report{}, Change: &change.Set{
		Files: []change.File{{Path: "a.go", Status: "modified", Diff: "--- a.go\n+x\n"}},
	}}
	got, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Prompt, "What the author says it does") {
		t.Error("an intent header with nothing under it")
	}
}
