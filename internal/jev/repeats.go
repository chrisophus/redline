package jev

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

// Repeats asks Jev whether a review finding says an earlier comment again. It
// is the review package's RepeatJudge.
type Repeats struct {
	Client *Client

	mu    sync.Mutex
	usage Usage
	calls int
}

// The questions. Every word Jev is shown for this decision is in this block.
//
// "Is it the same defect" is asked as two plainer things: whether the two
// comments are about the same piece of code, and whether they describe the
// same failure. The answer returned is the mean of the two probabilities.
//
// This is the second design. The first asked one question, "does it report
// the same underlying defect", with every earlier comment in the state under
// an id. Replayed over the same saved reviews and scored against 56
// hand-labelled findings, the first design ranked a finding said again above
// one that was not 91.9% of the time and this one 99% of the time. What moved
// the answers changed too. In the first design a comment that belonged to
// another question could carry a new case over the bar. Here a question is
// shown one earlier comment and the new one, so nothing else on the pull
// request can move it.
//
// The mean and not the lower of the two, because a repeat is often anchored
// on a different line of the same function and the code question then
// answers lower than the failure question does. On three repositories the
// questions were not written against, the mean put all four repeats at 0.85
// or more and the lower of the two put two of them at 0.78.
const (
	codeAsk  = "Is `new_comment` about the same function, check or statement as this earlier review comment?"
	failAsk  = "Does `new_comment` describe the same failure as this earlier review comment: the same thing going wrong, set off the same way?"
	failTrue = "Both comments name the same step failing or missing, set off by the same condition."
	failNot  = "The new comment names a different step, a different input, or a case the earlier comment did not mention."
)

// copies is how many times each question is asked in a request. Jev's answer
// to one question moves by about 0.02 either way from one asking to the next,
// and up to 0.07 was seen between two runs over the same reviews. A bar set
// between scores a few hundredths apart means nothing against that, so each
// question is asked three times and the answers averaged.
const copies = 3

// labelRE matches the "**Warning · agent** — " a posted thread opens with. It
// is how the comment was displayed and says nothing about the defect, so it
// is not sent.
var labelRE = regexp.MustCompile(`^\s*\*\*[^*]*\*\*\s*—\s*`)

// earlierName is the name an earlier comment rides under in a question.
const earlierName = "earlier_comment"

// perRequest bounds the earlier comments one request asks about. Each costs
// two questions asked three times, every one carrying the comment's text, and
// a request has to stay inside the endpoint's limit on its total size.
const perRequest = 10

type commentState struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text"`
}

// Same returns, for each earlier comment, the probability that the finding
// says it again. The state is the finding alone, and each earlier comment
// rides in the two questions asked about it.
func (r *Repeats) Same(ctx context.Context, c findings.ReviewComment, earlier []feedback.Thread) ([]float64, error) {
	out := make([]float64, len(earlier))
	state := map[string]any{"new_comment": commentState{File: c.File, Line: c.Line, Text: c.Body}}
	for from := 0; from < len(earlier); from += perRequest {
		to := min(from+perRequest, len(earlier))
		qs := make(map[string]Noul, 2*copies*(to-from))
		for i := from; i < to; i++ {
			t := earlier[i]
			with := commentState{File: t.File, Line: t.Line, Text: labelRE.ReplaceAllString(t.Said, "")}
			for n := range copies {
				qs[fmt.Sprintf("code%d.%d", i, n)] = Noul{Instructions: codeAsk, WithName: earlierName, With: with}
				qs[fmt.Sprintf("fail%d.%d", i, n)] = Noul{Instructions: failAsk, WithName: earlierName, With: with, True: failTrue, False: failNot}
			}
		}
		got, usage, err := r.Client.Ask(ctx, state, qs)
		r.mu.Lock()
		r.usage = r.usage.Add(usage)
		r.calls++
		r.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("asking Jev whether a finding was already said: %w", err)
		}
		if len(got) != len(qs) {
			// An answer that did not come back is not a no. Scoring it as
			// zero would make it one, so the whole comparison is refused.
			return nil, fmt.Errorf("jev answered %d of %d questions about whether a finding was already said", len(got), len(qs))
		}
		for i := from; i < to; i++ {
			sum := 0.0
			for n := range copies {
				sum += got[fmt.Sprintf("code%d.%d", i, n)] + got[fmt.Sprintf("fail%d.%d", i, n)]
			}
			out[i] = sum / (2 * copies)
		}
	}
	return out, nil
}

// Spent is what the questions asked so far used, and how many requests they
// went in.
func (r *Repeats) Spent() (Usage, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage, r.calls
}
