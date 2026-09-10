package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// Sampling exists because samples are disjoint: the union has to add them up
// rather than vote on them.
func TestSamplesUnionAddsDisjointFindings(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("end_turn", 100, 10, anthropicText(0,
			`{"overview":"a","files":[],"comments":[{"file":"a.go","line":1,"severity":"warning",`+
				`"confidence":"high","body":"the retry loop never bounds attempts"}]}`)),
		anthropicSSE("end_turn", 100, 10, anthropicText(0,
			`{"overview":"a","files":[],"comments":[{"file":"b.go","line":2,"severity":"warning",`+
				`"confidence":"high","body":"the mutex is never unlocked on the error path"}]}`)),
		anthropicSSE("end_turn", 100, 10, anthropicText(0,
			`{"overview":"a","files":[],"comments":[{"file":"c.go","line":3,"severity":"info",`+
				`"confidence":"low","body":"this comment contradicts the code below it"}]}`)),
	)
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	opts.Samples = 3
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(res.Review.Comments) != 3 {
		t.Fatalf("comments = %d, want all three samples kept: %+v", len(res.Review.Comments), res.Review.Comments)
	}
	if res.Samples != 3 || res.Turns != 3 {
		t.Errorf("samples = %d turns = %d, want 3 and 3", res.Samples, res.Turns)
	}
	// The cost is the union's, not one sample's, or the ledger under-reports
	// what was spent by a factor of Samples.
	if res.Usage.InputTokens != 300 || res.Usage.OutputTokens != 30 {
		t.Errorf("usage = %+v, want the three samples summed", res.Usage)
	}
}

// Several minutes of silence for a run that is working looks the same as one
// that has hung, and sampling multiplies the wait. Each sample says so as it
// lands.
func TestSamplesReportEachOneAsItLands(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("end_turn", 100, 10, anthropicText(0,
			`{"overview":"a","files":[],"comments":[{"file":"a.go","line":1,"severity":"warning",`+
				`"confidence":"high","body":"the retry loop never bounds attempts"}]}`)),
		anthropicSSE("max_tokens", 100, 10, anthropicText(0, `{"overview":"trunc`)),
	)
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	opts.Samples = 2
	var mu sync.Mutex
	var lines []string
	opts.Progress = func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, msg)
	}
	if _, err := Run(context.Background(), exploreInput(), opts); err != nil {
		t.Fatalf("one bad sample must not fail the review: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("got %d progress line(s), want one per sample: %v", len(lines), lines)
	}
	var reportedFailure bool
	for _, l := range lines {
		if strings.Contains(l, "failed") {
			reportedFailure = true
		}
		if !strings.Contains(l, "of 2") {
			t.Errorf("a progress line does not say how many samples there are: %q", l)
		}
	}
	if !reportedFailure {
		// A sample that failed still cost money and still thinned the union,
		// so it is reported rather than left as a gap in the count.
		t.Errorf("the failed sample was not reported: %v", lines)
	}
}

// Two samples that found the same defect in different words are one finding.
// Identity is the file plus the message with digits collapsed, so a line
// number that moved does not double-count.
func TestSamplesUnionCollapsesTheSameRemark(t *testing.T) {
	same := func(line int) string {
		return fmt.Sprintf(`{"overview":"a","files":[],"comments":[{"file":"a.go","line":%d,`+
			`"severity":"warning","confidence":"high","body":"the retry loop never bounds attempts, `+
			`so a 503 at line %d spins"}]}`, line, line)
	}
	api := serveSSE(t,
		anthropicSSE("end_turn", 100, 10, anthropicText(0, same(12))),
		anthropicSSE("end_turn", 100, 10, anthropicText(0, same(947))),
	)
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	opts.Samples = 2
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(res.Review.Comments) != 1 {
		t.Errorf("the same remark at two line numbers counted twice: %+v", res.Review.Comments)
	}
}

// A sample that fails is not a review that fails. The union is what the
// answering samples found, and the count of failures rides along so the
// report can say the union is thinner than it was paid for.
func TestSamplesSurviveOneFailure(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("end_turn", 100, 10, anthropicText(0,
			`{"overview":"a","files":[],"comments":[{"file":"a.go","line":1,"severity":"warning",`+
				`"confidence":"high","body":"the retry loop never bounds attempts"}]}`)),
		anthropicSSE("max_tokens", 100, 10, anthropicText(0, `{"overview":"trunc`)),
	)
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	opts.Samples = 2
	res, err := Run(context.Background(), exploreInput(), opts)
	if err != nil {
		t.Fatalf("one bad sample must not fail the review: %v", err)
	}
	if len(res.Review.Comments) != 1 {
		t.Errorf("the good sample was lost: %+v", res.Review.Comments)
	}
	if res.SamplesFailed != 1 {
		t.Errorf("samplesFailed = %d, want 1: a union of one paid for two has to say so", res.SamplesFailed)
	}
	if !strings.Contains(res.Summary(), "samples=2") || !strings.Contains(res.Summary(), "failed=1") {
		t.Errorf("the summary hides the failure: %s", res.Summary())
	}
}

// Every sample failing is a failed review, and the usage still has to be on
// the result: the calls were paid for whether or not they answered.
func TestSamplesAllFailingIsAnError(t *testing.T) {
	api := serveSSE(t, anthropicSSE("max_tokens", 100, 10, anthropicText(0, `{"overview":"trunc`)))
	opts := exploreOpts(api)
	opts.Mode = ModeOneShot
	opts.Samples = 2
	res, err := Run(context.Background(), exploreInput(), opts)
	if err == nil {
		t.Fatal("no sample answered, so there is no review")
	}
	if res.Usage.InputTokens != 200 {
		t.Errorf("usage = %+v, want both paid samples counted", res.Usage)
	}
}

// Explore already spends its budget on turns; sampling it multiplies a loop
// rather than taking independent readings, so it is refused rather than
// quietly costing several times what was asked for.
func TestSamplingExploreIsRefused(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 100, 10, anthropicText(0, exploreReviewJSON)))
	opts := exploreOpts(api)
	opts.Samples = 3
	if _, err := Run(context.Background(), exploreInput(), opts); err == nil {
		t.Fatal("sampling explore mode must be refused")
	}
}

// The union is not a vote: a finding one sample in three reported survives.
// Agreement cannot stand in for confidence here, because measurement found no
// overlap between samples at all, so a vote would discard everything.
func TestUnionIsNotAVote(t *testing.T) {
	rev := unionReviews([]*Result{
		{Review: findings.Review{Comments: []findings.ReviewComment{
			{File: "a.go", Body: "only this sample saw this", Confidence: findings.ConfidenceLow},
		}}},
		{Review: findings.Review{Comments: []findings.ReviewComment{
			{File: "b.go", Body: "and only this one saw that"},
		}}},
	})
	if len(rev.Comments) != 2 {
		t.Fatalf("comments = %d, want both singletons kept", len(rev.Comments))
	}
	if rev.Comments[0].Confidence != findings.ConfidenceLow {
		t.Errorf("the model's own confidence was rewritten to %q; agreement is not a confidence signal",
			rev.Comments[0].Confidence)
	}
}

// Two samples that found one defect describe it differently and ask the same
// thing about it. Prose could never merge them; the question can, which is
// what makes the zero-overlap measurement worth taking again.
func TestUnionMergesTwoWordingsOfOneQuestion(t *testing.T) {
	q := findings.Question{
		Kind: findings.QuestionPrecedent, Subject: "awsOfferFeedRawColumns",
		Ask: "does anything else in this repository hard-code a CopyFrom column list?",
	}
	a := &Result{Review: findings.Review{Comments: []findings.ReviewComment{{
		File: "feed.go", Body: "The hard-coded column list will drift from the table.", Question: q,
	}}}}
	b := &Result{Review: findings.Review{Comments: []findings.ReviewComment{{
		File: "feed.go", Body: "Columns are enumerated by hand here, so a migration can silently mis-stage rows.", Question: q,
	}}}}

	got := unionReviews([]*Result{a, b})
	if len(got.Comments) != 1 {
		t.Fatalf("one defect asked about twice is one finding: %+v", got.Comments)
	}
	// The longer phrasing survives, the way it always has.
	if !strings.Contains(got.Comments[0].Body, "mis-stage") {
		t.Fatalf("the fuller wording should win: %q", got.Comments[0].Body)
	}
}

// Two different defects in one file are two findings even when both carry a
// question, or the union would launder a real finding into silence.
func TestUnionKeepsTwoDefectsApartByTheirSubject(t *testing.T) {
	a := &Result{Review: findings.Review{Comments: []findings.ReviewComment{{
		File: "feed.go", Body: "column list drifts",
		Question: findings.Question{Kind: findings.QuestionPrecedent, Subject: "awsOfferFeedRawColumns"},
	}}}}
	b := &Result{Review: findings.Review{Comments: []findings.ReviewComment{{
		File: "feed.go", Body: "the idempotency check races",
		Question: findings.Question{Kind: findings.QuestionPrecedent, Subject: "ObjectAlreadyStaged"},
	}}}}
	if got := unionReviews([]*Result{a, b}); len(got.Comments) != 2 {
		t.Fatalf("two subjects are two findings: %+v", got.Comments)
	}
}

// A hand-written review has no question at all and must keep merging on prose
// the way it always did.
func TestUnionFallsBackToProseWithoutAQuestion(t *testing.T) {
	one := findings.ReviewComment{File: "a.go", Body: "this leaks on line 12"}
	two := findings.ReviewComment{File: "a.go", Body: "this leaks on line 947"}
	a := &Result{Review: findings.Review{Comments: []findings.ReviewComment{one}}}
	b := &Result{Review: findings.Review{Comments: []findings.ReviewComment{two}}}
	if got := unionReviews([]*Result{a, b}); len(got.Comments) != 1 {
		t.Fatalf("digit-normalised prose still merges: %+v", got.Comments)
	}
}

// A finding whose author says nothing would settle it is speculation by its
// own account, and the report and post already know what to do with a
// low-confidence finding.
func TestAQuestionOfNoneFoldsTheFindingAway(t *testing.T) {
	rev, err := parseReview([]byte(`{
      "overview": "x", "files": [], "verdicts": [],
      "comments": [{
        "file": "a.go", "line": 1, "severity": "warning", "confidence": "high",
        "category": "review", "relatedFindings": [], "body": "this feels wrong",
        "question": {"kind": "none", "ask": "", "subject": ""}
      }]
    }`))
	if err != nil {
		t.Fatal(err)
	}
	if got := rev.Comments[0].Confidence; got != findings.ConfidenceLow {
		t.Fatalf("confidence = %q; a finding nothing can settle must not post as certain", got)
	}
}

// The kinds are a closed set because the stages behind this act on them. One
// the scout cannot answer would be a lookup nobody can run.
func TestAnUnknownQuestionKindReadsAsUnstated(t *testing.T) {
	rev, err := parseReview([]byte(`{
      "overview": "x", "files": [], "verdicts": [],
      "comments": [{
        "file": "a.go", "line": 1, "severity": "warning", "confidence": "high",
        "category": "review", "relatedFindings": [], "body": "b",
        "question": {"kind": "vibes", "ask": "is this nice", "subject": "everything"}
      }]
    }`))
	if err != nil {
		t.Fatal(err)
	}
	if k := rev.Comments[0].Question.Kind; k != "" {
		t.Fatalf("kind = %q, want unstated", k)
	}
	if rev.Comments[0].Question.Answerable() {
		t.Fatal("an unstated question must not be sent to the scout as work")
	}
}
