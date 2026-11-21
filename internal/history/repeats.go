package history

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
)

// Replayed is what the pass that holds back repeated findings would have done
// on one saved review.
type Replayed struct {
	// Earlier is how many earlier comments on the pull request the findings
	// were compared with, and Unshown how many of those the review itself
	// had not been shown, because they were only ever in a review's summary.
	Earlier int `json:"earlier"`
	Unshown int `json:"unshown"`
	review.Repeats
	// Findings is the text of each finding compared, by id, for a reader
	// checking the verdicts by hand.
	Findings map[string]string `json:"findings,omitempty"`
}

// RepeatTotals is the replay summed over every review it ran on.
type RepeatTotals struct {
	Reviews   int    `json:"reviews"`
	Findings  int    `json:"findings"`
	Held      int    `json:"held"`
	FollowUps int    `json:"followUps"`
	Failed    string `json:"failed,omitempty"`
}

// labelRE matches the "**Warning · agent** — " a posted comment opens with.
var labelRE = regexp.MustCompile(`^\*\*[^*]*\*\*\s*—\s*`)

// sameText reports whether a saved finding and a posted comment are the same
// text. A comment is posted as the finding with a label in front, so the first
// sixty characters after the label are compared.
func sameText(finding, posted string) bool {
	a := key(finding)
	b := key(labelRE.ReplaceAllString(strings.TrimSpace(posted), ""))
	return a != "" && a == b
}

func key(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// ReplayRepeats runs the repeats pass over every saved review that was not
// the first of its pull request, with what that review could have known.
//
// The earlier comments for a review are the threads its own session recorded,
// with the replies they had then, and every finding of an earlier review of
// the same pull request that is not among those threads. The second kind is
// what a review's summary carried, and it is an approximation: a saved trace
// does not say whether a finding was folded into the summary or withheld
// altogether. progress, when not nil, is called after each review.
func (rep *Report) ReplayRepeats(ctx context.Context, judge review.RepeatJudge, progress func(done, total int)) {
	bySession := map[string][]int{}
	for i, r := range rep.Runs {
		bySession[r.Session] = append(bySession[r.Session], i)
	}
	var later [][2]int // run index, position in its session
	for _, idx := range bySession {
		sort.SliceStable(idx, func(a, b int) bool { return rep.Runs[idx[a]].When.Before(rep.Runs[idx[b]].When) })
		for pos := 1; pos < len(idx); pos++ {
			later = append(later, [2]int{idx[pos], pos})
		}
	}
	sort.Slice(later, func(a, b int) bool { return later[a][0] < later[b][0] })

	tot := &RepeatTotals{}
	rep.Repeats = tot
	for n, l := range later {
		r := &rep.Runs[l[0]]
		trace, err := postmortem.Load(r.Dir)
		if err != nil || len(trace.Findings) == 0 {
			continue
		}
		var before []postmortem.Finding
		for _, i := range bySession[r.Session][:l[1]] {
			if t, err := postmortem.Load(rep.Runs[i].Dir); err == nil {
				before = append(before, t.Findings...)
			}
		}
		prior, unshown := priorFor(r.Dir, before)
		if len(prior) == 0 {
			continue
		}
		rev := findings.Review{}
		text := map[string]string{}
		for i, f := range trace.Findings {
			rev.Comments = append(rev.Comments, findings.ReviewComment{
				File: f.File, Line: f.Line, Severity: f.Severity, Confidence: f.Confidence, Body: f.Body})
			text[fmt.Sprintf("c%d", i+1)] = f.Body
		}
		_, did := review.MarkRepeats(ctx, judge, rev, prior)
		r.Replayed = &Replayed{Earlier: len(prior), Unshown: unshown, Repeats: did, Findings: text}
		tot.Reviews++
		tot.Findings += did.Checked
		tot.Held += did.Held
		tot.FollowUps += did.FollowUps
		if did.Failed != "" && tot.Failed == "" {
			tot.Failed = did.Failed
		}
		if progress != nil {
			progress(n+1, len(later))
		}
	}
}

// priorFor is what a review could have been told: the threads its session
// recorded, then each earlier finding that is not one of them. The count is
// how many of the second kind there are.
func priorFor(dir string, before []postmortem.Finding) ([]feedback.Thread, int) {
	var prior []feedback.Thread
	if res, err := run.LoadSession(dir); err == nil {
		prior = append(prior, res.PriorReview...)
	}
	unshown := 0
	for _, f := range before {
		known := false
		for _, t := range prior {
			if sameText(f.Body, t.Said) || (t.InBody && key(f.Body) == key(t.Said)) {
				known = true
				break
			}
		}
		if known || strings.TrimSpace(f.Body) == "" {
			continue
		}
		prior = append(prior, feedback.Thread{File: f.File, Line: f.Line, Said: f.Body, InBody: true})
		unshown++
	}
	return prior, unshown
}

// repeats writes the replay's section of the report.
func (rep *Report) repeats(b *strings.Builder) {
	tot := rep.Repeats
	if tot == nil {
		return
	}
	b.WriteString("\nFindings that said an earlier one again\n")
	if tot.Reviews == 0 {
		b.WriteString("  No pull request here was reviewed more than once with findings to compare.\n")
		return
	}
	fmt.Fprintf(b, "  %s of a pull request already reviewed wrote %s.\n",
		plural(tot.Reviews, "review"), plural(tot.Findings, "finding"))
	fmt.Fprintf(b, "    %s said again something nobody had answered as fixed, and would be held back\n",
		share(tot.Held, tot.Findings))
	fmt.Fprintf(b, "    %s said again something answered as fixed, and would be kept\n",
		share(tot.FollowUps, tot.Findings))
	var fromBody, held int
	for _, r := range rep.Runs {
		if r.Replayed == nil {
			continue
		}
		for _, s := range r.Replayed.Seen {
			if s.Verdict == review.RepeatHeld {
				held++
				if s.InBody {
					fromBody++
				}
			}
		}
	}
	if held > 0 {
		fmt.Fprintf(b, "  %s of the held findings repeat one that was only in a review's summary,\n"+
			"  which the review that repeated it had not been shown.\n", share(fromBody, held))
	}
	if tot.Failed != "" {
		fmt.Fprintf(b, "  The replay stopped early on at least one review: %s\n", tot.Failed)
	}
	fmt.Fprintf(b, "  A finding is held at a probability of %.1f or more that it is the same defect.\n", review.RepeatBar)
}
