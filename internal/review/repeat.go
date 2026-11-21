package review

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

// RepeatBar is how sure the judge has to be that a finding says an earlier one
// again before the finding is held back.
//
// It was set against 56 findings from later reviews of pull requests in one
// repository, each labelled by hand as an earlier finding said again, a new
// case of the same kind of mistake, or unrelated. At 0.85 the judge put 19 of
// the 24 said again over the bar and none of the 32 others, on two runs. At
// 0.8 it put 22 to 24 of them over and two of the others. A new case is the
// finding an author most needs, so the bar sits where it loses a repeat
// before it loses one of those. On three repositories it was not set against,
// three of four repeats were over it and no other finding scored above 0.40.
//
// Two answers to one question differ by a few hundredths, so a finding within
// that distance of the bar can land either side of it from run to run.
const RepeatBar = 0.85

// maxRepeatEarlier bounds how many earlier comments one finding is compared
// with. A pull request with more than this has a conversation no reader is
// following either, and the ones kept are those in the finding's own file.
const maxRepeatEarlier = 60

// RepeatJudge says how likely it is that a finding reports the same defect as
// each of the earlier comments, one probability per comment in the order
// given. It is the one judgement in this file a rule cannot make: two reviews
// of one pull request word the same defect differently, and nothing keyed on
// the wording can follow that.
type RepeatJudge interface {
	Same(ctx context.Context, c findings.ReviewComment, earlier []feedback.Thread) ([]float64, error)
}

// Repeats is what the pass that looks for repeated findings did.
type Repeats struct {
	// Checked is how many findings were compared with earlier comments and
	// Held how many were ruled already raised.
	Checked int `json:"checked,omitempty"`
	Held    int `json:"held,omitempty"`
	// FollowUps are findings that say again something the author answered as
	// fixed. Those are kept: a reviewer that read the new code and still sees
	// the defect is reporting that the fix did not land.
	FollowUps int `json:"followUps,omitempty"`
	// Failed is why the pass stopped early, empty when it did not.
	Failed string `json:"failed,omitempty"`
	// Seen is the closest earlier comment for each finding compared, in the
	// order the review wrote the findings.
	Seen []RepeatSeen `json:"seen,omitempty"`
}

// RepeatSeen is one finding and the earlier comment it came closest to.
type RepeatSeen struct {
	// ID is the finding's c1, c2 id.
	ID string `json:"id"`
	// Same is the probability that the two report the same defect.
	Same float64 `json:"same"`
	// Earlier is where the earlier comment was, and InBody whether it was in a
	// review's summary and not on a thread.
	Earlier string `json:"earlier,omitempty"`
	InBody  bool   `json:"inBody,omitempty"`
	// Verdict is held, follow-up, or empty for a finding under the bar.
	Verdict string `json:"verdict,omitempty"`
}

// What a compared finding was ruled.
const (
	RepeatHeld     = "held"
	RepeatFollowUp = "follow-up"
)

// MarkRepeats runs the pass on its own, for a caller replaying saved reviews.
func MarkRepeats(ctx context.Context, judge RepeatJudge, rev findings.Review, prior []feedback.Thread) (findings.Review, Repeats) {
	res := &Result{Review: rev}
	markRepeats(ctx, Input{Prior: prior}, Options{Repeats: judge}, res)
	return res.Review, res.Repeats
}

// markRepeats rules a finding already raised when it says again something
// this pull request has on record and nobody claimed to have fixed.
//
// It never fails the review. A judge that errors leaves every finding it had
// not reached as the reviewer wrote it, which is what happened before this
// pass existed.
func markRepeats(ctx context.Context, in Input, opts Options, res *Result) {
	if opts.Repeats == nil || res == nil || len(in.Prior) == 0 {
		return
	}
	cands := Candidates(res.Review)
	rulings := map[string]findings.Ruling{}
	for _, c := range cands {
		if c.Comment.Ruling.Verdict != "" {
			continue
		}
		earlier := earlierFor(c.Comment, in.Prior)
		probs, err := opts.Repeats.Same(ctx, c.Comment, earlier)
		if err != nil {
			res.Repeats.Failed = err.Error()
			break
		}
		res.Repeats.Checked++
		best, at := 0.0, -1
		for i, p := range probs {
			if i < len(earlier) && p > best {
				best, at = p, i
			}
		}
		if at < 0 {
			continue
		}
		t := earlier[at]
		seen := RepeatSeen{ID: c.ID, Same: best, Earlier: locOf(t), InBody: t.InBody}
		if best < RepeatBar {
			res.Repeats.Seen = append(res.Repeats.Seen, seen)
			continue
		}
		if claimedFixed(t) {
			res.Repeats.FollowUps++
			seen.Verdict = RepeatFollowUp
			res.Repeats.Seen = append(res.Repeats.Seen, seen)
			continue
		}
		res.Repeats.Held++
		seen.Verdict = RepeatHeld
		res.Repeats.Seen = append(res.Repeats.Seen, seen)
		rulings[c.ID] = findings.Ruling{
			Verdict:  findings.VerifiedAlreadyRaised,
			Evidence: repeatEvidence(t),
			Why: fmt.Sprintf("this says again what an earlier review of this pull request said "+
				"(probability %.2f that it is the same defect), and nobody has said that one was fixed", best),
		}
	}
	if len(rulings) > 0 {
		res.Review = Apply(res.Review, cands, rulings)
	}
}

// earlierFor picks the earlier comments a finding is compared with: all of
// them when they fit, and otherwise the ones in the finding's file first.
func earlierFor(c findings.ReviewComment, prior []feedback.Thread) []feedback.Thread {
	if len(prior) <= maxRepeatEarlier {
		return prior
	}
	out := make([]feedback.Thread, 0, maxRepeatEarlier)
	for _, t := range prior {
		if t.File == c.File && len(out) < maxRepeatEarlier {
			out = append(out, t)
		}
	}
	for _, t := range prior {
		if t.File != c.File && len(out) < maxRepeatEarlier {
			out = append(out, t)
		}
	}
	return out
}

// declinedRE matches the reply an author writes to turn a finding down. It is
// the same phrase `redline dispositions` reads.
var declinedRE = regexp.MustCompile(`(?i)^\s*(\*\*)?\s*not fixing`)

// claimedFixed reports whether somebody answered the earlier comment in a way
// that says it was dealt with. A summary finding and an unanswered thread were
// not, unless the thread was closed or its lines have changed since. A thread
// whose last reply turns the finding down was not either: the
// author's position is on the record and saying the finding again does not
// change it. Anything else that was answered or closed counts as a claim that
// it was fixed, because that is the case where holding the finding back could
// hide a defect.
func claimedFixed(t feedback.Thread) bool {
	if t.InBody {
		return false
	}
	if len(t.Replies) == 0 {
		// Closed without a word, or the lines it sat on have changed since:
		// either may be the author's fix, made without saying so.
		return t.Resolved || t.Outdated
	}
	return !declinedRE.MatchString(t.Replies[len(t.Replies)-1].Body)
}

func locOf(t feedback.Thread) string {
	if t.Line > 0 {
		return fmt.Sprintf("%s:%d", t.File, t.Line)
	}
	return t.File
}

func repeatEvidence(t feedback.Thread) string {
	loc := locOf(t)
	where := "an earlier thread at " + loc
	if t.InBody {
		where = "an earlier review's summary, on " + loc
	}
	said := oneLine(t.Said)
	if len(said) > 300 {
		said = strings.TrimSpace(said[:300]) + "..."
	}
	return fmt.Sprintf("%s: %s", where, said)
}
