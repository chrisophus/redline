package review

import (
	"context"
	"fmt"
	"strings"
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
