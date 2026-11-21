package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
)

// sameWords is a judge that calls two comments the same defect when they share
// their first word.
type sameWords struct{}

func (sameWords) Same(_ context.Context, c findings.ReviewComment, earlier []feedback.Thread) ([]float64, error) {
	first := func(s string) string {
		s = labelRE.ReplaceAllString(s, "")
		return strings.Fields(s)[0]
	}
	out := make([]float64, len(earlier))
	for i, t := range earlier {
		if first(c.Body) == first(t.Said) {
			out[i] = 0.95
		}
	}
	return out, nil
}

// replayFixture is one pull request reviewed at three commits. The first
// review wrote two findings. One became a thread the author answered as
// fixed, and the other was only ever in the summary.
func replayFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pr := filepath.Join(root, sessionsDir, "pr-7")
	heads := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}
	thread := feedback.Thread{File: "a.go", Line: 4, Said: "**Warning · agent** — lock held over a network call",
		Resolved: true, Replies: []feedback.Reply{{Author: "dev", Body: "Addressed in 1234567."}}}
	write := func(dir, head string, at time.Time, prior []feedback.Thread, bodies ...string) {
		tr := trace(head, at)
		for i, b := range bodies {
			tr.Findings = append(tr.Findings, postmortem.Finding{ID: "c" + string(rune('1'+i)), File: "a.go", Line: 4, Body: b})
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		res := &run.Result{Report: findings.Report{BaseSHA: base}, Change: set(head), PriorReview: prior}
		if err := run.SaveSession(dir, res); err != nil {
			t.Fatal(err)
		}
		if err := postmortem.Write(dir, tr); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(pr, heads[0]), heads[0], day, nil,
		"lock held over a network call", "names compare by exact case")
	write(filepath.Join(pr, heads[1]), heads[1], day.Add(time.Hour), []feedback.Thread{thread},
		"names are matched case-sensitively", "timeout is never set")
	write(filepath.Join(pr, heads[2]), heads[2], day.Add(2*time.Hour), []feedback.Thread{thread},
		"lock is still held over the call", "names differ only by case")
	return root
}

func TestReplayRepeats(t *testing.T) {
	rep, err := Collect(replayFixture(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 3 {
		t.Fatalf("want three reviews, got %d", len(rep.Runs))
	}
	rep.ReplayRepeats(context.Background(), sameWords{}, nil)

	if rep.Runs[0].Replayed != nil {
		t.Error("a pull request's first review has nothing to repeat")
	}
	tot := rep.Repeats
	if tot.Reviews != 2 || tot.Findings != 4 || tot.Held != 2 || tot.FollowUps != 1 {
		t.Fatalf("totals = %+v", tot)
	}
	second := rep.Runs[1].Replayed
	// The thread, plus the summary finding the second review was never shown.
	if second.Earlier != 2 || second.Unshown != 1 {
		t.Errorf("second review: earlier=%d unshown=%d", second.Earlier, second.Unshown)
	}
	if second.Held != 1 || second.Seen[0].Verdict != review.RepeatHeld || !second.Seen[0].InBody {
		t.Errorf("second review: %+v", second.Repeats)
	}
	third := rep.Runs[2].Replayed
	if third.Held != 1 || third.FollowUps != 1 {
		t.Errorf("third review: %+v", third.Repeats)
	}

	text := rep.Render()
	for _, want := range []string{
		"Findings that said an earlier one again",
		"2 reviews of a pull request already reviewed wrote 4 findings",
		"2 of 4 (50%) said again something nobody had answered as fixed",
		"1 of 4 (25%) said again something answered as fixed",
		"2 of 2 (100%) of the held findings repeat one that was only in a review's summary",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report is missing %q:\n%s", want, text)
		}
	}
}

func TestReplayWithNothingReviewedTwice(t *testing.T) {
	rep := collect(t)
	for i := range rep.Runs {
		rep.Runs[i].Session = "pr-" + string(rune('a'+i))
	}
	rep.ReplayRepeats(context.Background(), sameWords{}, nil)
	if !strings.Contains(rep.Render(), "No pull request here was reviewed more than once") {
		t.Errorf("report:\n%s", rep.Render())
	}
}
