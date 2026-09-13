package review

import (
	"strings"
	"testing"
)

// A reply can satisfy the tool grammar and still not be a review. The schemas
// require the four fields to be present and say nothing about what is in them,
// so three empty arrays under a one-word overview validates on the wire and
// arrives looking like a review that found nothing.
//
// It was measured rather than imagined: on a 48k packet the forced tool_choice
// arm returned {"overview": "placeholder"} with empty files, comments and
// verdicts on three samples out of three, and the eval scored it zero of
// sixteen labels. A recall number is the wrong way to report a call that never
// started, so absorb refuses it and the run fails loudly instead.
const stubReply = `{"overview":"placeholder","files":[],"comments":[],"verdicts":[]}`

func TestAConformingReplyThatReviewedNothingIsRefused(t *testing.T) {
	res := &Result{Stage: StageReview, FilesShown: 12}
	err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: stubReply, stopReason: "end_turn",
	})
	if err == nil {
		t.Fatal("a review with no file lines, no comments and no verdicts was accepted")
	}
	if !strings.Contains(err.Error(), "12 file(s)") {
		t.Errorf("the error does not say what was asked for: %v", err)
	}
}

// The packet is what makes the refusal safe to make. A call that showed no
// files has nothing to write a file line about, so an empty answer to it is
// not evidence of anything and must not be turned into a failed run.
func TestAnEmptyReplyToAnEmptyPacketIsKept(t *testing.T) {
	res := &Result{Stage: StageReview}
	if err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: stubReply, stopReason: "end_turn",
	}); err != nil {
		t.Fatalf("refused a reply to a packet that showed no files: %v", err)
	}
}

// The answer this must never take away. Zero comments is correct on a change
// with no defects, and the whole point of scoring quiet fixtures is that a
// reviewer can reach it. One file line is enough to show the call ran.
func TestACleanReviewWithNoCommentsIsKept(t *testing.T) {
	res := &Result{Stage: StageReview, FilesShown: 1}
	err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: `{"overview":"a rename and nothing else",
			"files":[{"path":"internal/store/user.go","summary":"renames the receiver"}],
			"comments":[],"verdicts":[]}`,
		stopReason: "end_turn",
	})
	if err != nil {
		t.Fatalf("refused a clean review: %v", err)
	}
	if len(res.Review.Comments) != 0 {
		t.Errorf("comments = %v, want none", res.Review.Comments)
	}
	if len(res.Review.Files) != 1 {
		t.Errorf("files = %v, want the one line", res.Review.Files)
	}
}
