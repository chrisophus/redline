package eval

import (
	"fmt"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/post"
)

// Scoring the review a model wrote and scoring the review a reader receives
// used to be the same thing. They are not any more, and the difference is the
// whole point of everything built since the field round.
//
// A comment can now be written and never delivered. The reviewer said it was
// unsure, or its own wording disqualified it, or the verifying pass withdrew
// it, or this pull request had already heard it and somebody answered. A score
// taken before those gates measures a producer nobody talks to.
//
// Which makes this the number that decides whether any of it worked, and it
// cuts both ways. Suppression that removes a labelled defect is a regression
// however much noise it removes with it, and a pass tuned only against
// precision will do exactly that: withdraw everything, score perfectly, be
// worthless. So the two are reported side by side and neither is allowed to
// stand alone.

// Delivered is what a reader actually receives from one review.
type Delivered struct {
	// Interrupts are the comments that open a thread on the diff.
	Interrupts []findings.ReviewComment
	// InBody are the comments that reach the reader without interrupting: the
	// reviewer's info findings, read once by whoever reads the review.
	InBody []findings.ReviewComment
	// Suppressed is everything that reached nobody, by the reason it did not.
	Suppressed map[string][]findings.ReviewComment
}

// All is everything that reached the reader, in either form.
func (d Delivered) All() []findings.ReviewComment {
	out := make([]findings.ReviewComment, 0, len(d.Interrupts)+len(d.InBody))
	out = append(out, d.Interrupts...)
	return append(out, d.InBody...)
}

// SuppressedCount is how many comments never reached anyone.
func (d Delivered) SuppressedCount() int {
	n := 0
	for _, cs := range d.Suppressed {
		n += len(cs)
	}
	return n
}

// Deliver runs a review through the same gates `post` applies, and says what
// became of every comment.
//
// It goes through findings.Review's own conversion and post's own predicates
// rather than reimplementing either. A scoring function with its own copy of
// "what reaches an author" would eventually measure a rule the product does
// not have, and the number would be worse than no number because it would look
// like evidence.
func Deliver(rev findings.Review) Delivered {
	d := Delivered{Suppressed: map[string][]findings.ReviewComment{}}
	fs := rev.CommentFindings()
	for i, f := range fs {
		if i >= len(rev.Comments) {
			break
		}
		c := rev.Comments[i]
		switch {
		case !post.Reaches(f):
			d.Suppressed[suppressionReason(c)] = append(d.Suppressed[suppressionReason(c)], c)
		case post.Interrupts(f):
			d.Interrupts = append(d.Interrupts, c)
		default:
			d.InBody = append(d.InBody, c)
		}
	}
	return d
}

// suppressionReason names why a comment did not reach the reader, in the words
// the thing that stopped it would use.
//
// The ruling's own verdict when there is one, because that is the specific
// answer and the confidence it set is only its consequence. "Unsure" and
// "hedged" are what is left when nothing ruled.
func suppressionReason(c findings.ReviewComment) string {
	if v := c.Ruling.Verdict; v != "" && v != findings.VerifiedKept {
		return v
	}
	// The question, not only the confidence field. A comment whose author said
	// nothing would settle it is speculation by that account, and the
	// demotion that follows happens during conversion rather than on the
	// comment, so reading the field alone would file it under the wrong gate.
	if c.Question.Kind == findings.QuestionNone || c.Confidence == findings.ConfidenceLow {
		return "unsure"
	}
	return "hedged"
}

// ScoreDelivered scores the review a reader receives rather than the one a
// model wrote, and reports what the gates cost in recall.
//
// Two scorecards fall out and both are needed. The delivered one is what the
// reader's experience is. The written one is what the producer is capable of,
// and the difference between their Caught sets is the bar the plan sets for
// the verifying pass: a labelled defect the pipeline suppressed is a
// regression, whatever it did to the noise.
func ScoreDelivered(f Fixture, rev findings.Review) (delivered, written Scorecard, lost []string) {
	d := Deliver(rev)
	delivered = Score(f, findings.Review{
		Overview: rev.Overview, Files: rev.Files, Comments: d.All(),
	})
	written = Score(f, rev)

	got := map[string]bool{}
	for _, key := range delivered.Caught {
		got[key] = true
	}
	for _, key := range written.Caught {
		if !got[key] {
			lost = append(lost, key)
		}
	}
	return delivered, written, lost
}

// DeliveryReport renders what the gates did to one review, for a reader
// deciding whether the trade was worth it.
func DeliveryReport(name string, d Delivered, lost []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d on the diff, %d in the body, %d suppressed",
		name, len(d.Interrupts), len(d.InBody), d.SuppressedCount())
	if n := d.SuppressedCount(); n > 0 {
		var parts []string
		for _, reason := range []string{
			findings.VerifiedWithdrawn, findings.VerifiedJustified,
			findings.VerifiedAlreadyRaised, findings.VerifiedUnverifiable,
			"unsure", "hedged",
		} {
			if k := len(d.Suppressed[reason]); k > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", k, reason))
			}
		}
		fmt.Fprintf(&b, " (%s)", strings.Join(parts, ", "))
	}
	if len(lost) > 0 {
		// Named rather than counted. This is the number that decides whether
		// the suppression was worth having, so a reader has to be able to go
		// and look at the defect it cost them.
		fmt.Fprintf(&b, "; LOST %d labelled defect(s): %s", len(lost), strings.Join(lost, ", "))
	}
	return b.String()
}
