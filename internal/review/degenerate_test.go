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

// Removal is per comment. A review that carries a stub beside a real comment
// keeps the real one and loses the stub, and the log line says how many went.
func TestAStubCommentBesideARealOneIsDroppedAndCounted(t *testing.T) {
	res := &Result{Stage: StageReview, FilesShown: 1}
	err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: `{"overview":"adds a nullable column",
			"files":[{"path":"internal/store/user.go","summary":"adds the Note field"}],
			"comments":[
				{"file":"x","line":1,"severity":"info","body":"placeholder"},
				{"file":"internal/store/user.go","line":0,"severity":"info","body":""},
				{"file":"internal/store/user.go","line":12,"severity":"warning","body":"Insert never writes Note."}
			],"verdicts":[]}`,
		stopReason: "end_turn",
	})
	if err != nil {
		t.Fatalf("refused a review with one real comment: %v", err)
	}
	if len(res.Review.Comments) != 1 || res.Review.Comments[0].Line != 12 {
		t.Errorf("comments = %+v, want only the real one", res.Review.Comments)
	}
	if res.Stubs != 2 {
		t.Errorf("Stubs = %d, want 2", res.Stubs)
	}
	if !strings.Contains(res.Summary(), "stubs=2") {
		t.Errorf("the log line does not say comments were dropped: %s", res.Summary())
	}
}

// The shape the claude-sonnet-5 baseline returned five times in 42 calls: a
// one-word overview, no file lines, no verdicts and a single placeholder
// comment. That comment carried it past the whole-reply check and it was scored
// as a review. With the stub removed first, it is refused.
func TestAReplyOfOnlyStubsIsStillRefused(t *testing.T) {
	res := &Result{Stage: StageReview, FilesShown: 3}
	err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: `{"overview":"placeholder","files":[],
			"comments":[{"file":"x","line":1,"severity":"info","body":"placeholder"}],
			"verdicts":[]}`,
		stopReason: "end_turn",
	})
	if err == nil {
		t.Fatal("a reply whose only comment was a stub was accepted as a review")
	}
}

// The other two of the seven carried a file line, and its summary was
// "placeholder" as well. A file line has to say something to count as reviewing.
func TestAStubFileLineDoesNotCountAsReviewing(t *testing.T) {
	res := &Result{Stage: StageReview, FilesShown: 1}
	err := res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: `{"overview":"placeholder",
			"files":[{"path":"api/schema.proto","summary":"placeholder"}],
			"comments":[{"file":"api/schema.proto","line":5,"severity":"info","body":"placeholder"}],
			"verdicts":[]}`,
		stopReason: "end_turn",
	})
	if err == nil {
		t.Fatal("a reply of stubs with one stub file line was accepted as a review")
	}
	if res.Stubs != 2 {
		t.Errorf("Stubs = %d, want the comment and the file line", res.Stubs)
	}
}

// The line is one word. A remark of two words says something, and dropping it
// would trade a padding problem for a recall one.
func TestATwoWordCommentIsKept(t *testing.T) {
	rev, err := ParseReply(`{"overview":"o","files":[],
		"comments":[{"file":"a.go","line":1,"severity":"info","body":"Unused import."}],
		"verdicts":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Comments) != 1 {
		t.Errorf("comments = %+v, want the two-word remark kept", rev.Comments)
	}
}
