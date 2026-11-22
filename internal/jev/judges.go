package jev

import (
	"context"
	"fmt"
	"sync"
)

// The three judges in this file each ask one yes/no question about every item
// in a list, against one state. They follow what the repeats judge found out:
// an item rides in its own question so that nothing belonging to another
// question can move the answer, and each question is asked a few times and
// averaged because one answer moves by a few hundredths between askings.
//
// The repeats questions were scored against labelled cases. Of these three,
// only the misses question has been, on 2026-11-22: on 17 confirmed fixes it
// ranked the right finding first every time, and 14 of 17 scored 0.5 or more.
// A miss summary has to name the defect for that to hold. The evidence
// question is unscored: reviews run without --verify have no ruling to
// ask about, and it checks whether a quote matches a claim, not whether the
// finding is right. The context question is unscored too: "used" is judged
// only from ruling quotes and symbol names, so few reviews have both used and
// unused entries. `redline history` replays them over saved reviews.

// meter counts what a judge's requests used.
type meter struct {
	mu    sync.Mutex
	usage Usage
	calls int
}

func (m *meter) add(u Usage) {
	m.mu.Lock()
	m.usage = m.usage.Add(u)
	m.calls++
	m.mu.Unlock()
}

// Spent is what the questions asked so far used, and how many requests they
// went in.
func (m *meter) Spent() (Usage, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage, m.calls
}

// askEach asks q about each item, with the item attached to its own question
// under name, and returns one averaged probability per item. Items go batch
// to a request. A reply missing any answer is refused: an answer that did not
// come back is not a no.
func askEach(ctx context.Context, c *Client, m *meter, state any, q Noul, name string, items []any, batch int) ([]float64, error) {
	out := make([]float64, len(items))
	for from := 0; from < len(items); from += batch {
		to := min(from+batch, len(items))
		qs := make(map[string]Noul, copies*(to-from))
		for i := from; i < to; i++ {
			for n := range copies {
				one := q
				one.WithName, one.With = name, items[i]
				qs[fmt.Sprintf("q%d.%d", i, n)] = one
			}
		}
		got, usage, err := c.Ask(ctx, state, qs)
		m.add(usage)
		if err != nil {
			return nil, err
		}
		if len(got) != len(qs) {
			return nil, fmt.Errorf("jev answered %d of %d questions", len(got), len(qs))
		}
		for i := from; i < to; i++ {
			sum := 0.0
			for n := range copies {
				sum += got[fmt.Sprintf("q%d.%d", i, n)]
			}
			out[i] = sum / copies
		}
	}
	return out, nil
}

// Said is one review finding, as a judge is shown it.
type Said struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text"`
}

// Defect is a bug that surfaced after a review, in the words of whoever
// recorded it.
type Defect struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Summary string `json:"summary"`
}

// Misses asks whether a review finding describes a defect that was recorded
// later as one the review let through.
type Misses struct {
	Client *Client
	meter
}

const (
	missAsk  = "Does this review finding describe `defect`: the same thing going wrong in the same code?"
	missTrue = "The finding points at the same code and names the same failure, even in different words or a few lines away."
	missNot  = "The finding is about other code, or about a different problem in the same code."
)

// Describes returns, for each finding, the probability that it describes the
// defect.
func (j *Misses) Describes(ctx context.Context, d Defect, said []Said) ([]float64, error) {
	items := make([]any, len(said))
	for i, s := range said {
		items[i] = s
	}
	q := Noul{Instructions: missAsk, True: missTrue, False: missNot}
	out, err := askEach(ctx, j.Client, &j.meter, map[string]any{"defect": d}, q, "finding", items, perRequest)
	if err != nil {
		return nil, fmt.Errorf("asking Jev whether a finding describes a recorded defect: %w", err)
	}
	return out, nil
}

// Claim is a finding and the line of code a ruling quoted as its evidence.
type Claim struct {
	Finding  Said   `json:"finding"`
	Evidence string `json:"evidence"`
}

// Evidence asks whether the code a ruling quoted shows what the finding
// claims. A ruling is already checked for having quoted something that was in
// front of it. That check passes for a real line that does not say what the
// finding says it does, and this is the question that check cannot ask.
type Evidence struct {
	Client *Client
	meter
}

const (
	evidenceAsk  = "Does `evidence`, a line quoted from the code, show the problem that `finding` describes?"
	evidenceTrue = "The quoted code is the code the finding is about, and reading it shows the problem the finding names."
	evidenceNot  = "The quoted code is unrelated to the finding, or it is the right code and does not show the problem."
)

// Supports returns the probability that the claim's evidence shows what its
// finding says. The state is the claim alone, so one claim goes in a request.
func (j *Evidence) Supports(ctx context.Context, c Claim) (float64, error) {
	qs := make(map[string]Noul, copies)
	for n := range copies {
		qs[fmt.Sprintf("q%d", n)] = Noul{Instructions: evidenceAsk, True: evidenceTrue, False: evidenceNot}
	}
	got, usage, err := j.Client.Ask(ctx, c, qs)
	j.add(usage)
	if err != nil {
		return 0, fmt.Errorf("asking Jev whether evidence supports a finding: %w", err)
	}
	if len(got) != len(qs) {
		return 0, fmt.Errorf("jev answered %d of %d questions about a finding's evidence", len(got), len(qs))
	}
	sum := 0.0
	for _, p := range got {
		sum += p
	}
	return sum / copies, nil
}

// Changed is one file of the change under review.
type Changed struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Diff    string `json:"diff,omitempty"`
}

// Entry is one piece of resolved context a review could be shown: a caller,
// a type, the history of a deleted line.
type Entry struct {
	Role    string `json:"role"`
	File    string `json:"file,omitempty"`
	Symbol  string `json:"symbol,omitempty"`
	Content string `json:"content"`
}

// Context asks which context entries a reviewer would need for a change.
type Context struct {
	Client *Client
	meter
}

const (
	contextAsk  = "Would a reviewer need to read this context entry to judge whether `change` is correct?"
	contextTrue = "The entry is code the change calls, is called by, or depends on in a way the change could break."
	contextNot  = "The entry is near the change and nothing the change does could be judged differently for having read it."
)

// The sizes Needed cuts to, so a request stays inside the endpoint's limits
// of 64k tokens for a request and 32k for the state with any one question.
// An entry is sent three times, once per asking.
const (
	contextDiffPerFile = 2000
	contextDiffTotal   = 30000
	contextEntryChars  = 3000
	contextPerRequest  = 8
)

// Needed returns, for each entry, the probability that a reviewer would need
// it to judge the change. Diffs and entries are cut to fit.
func (j *Context) Needed(ctx context.Context, files []Changed, entries []Entry) ([]float64, error) {
	left := contextDiffTotal
	state := make([]Changed, len(files))
	for i, f := range files {
		f.Diff = clipRunes(f.Diff, min(contextDiffPerFile, left))
		left -= len(f.Diff)
		state[i] = f
	}
	items := make([]any, len(entries))
	for i, e := range entries {
		e.Content = clipRunes(e.Content, contextEntryChars)
		items[i] = e
	}
	q := Noul{Instructions: contextAsk, True: contextTrue, False: contextNot}
	out, err := askEach(ctx, j.Client, &j.meter, map[string]any{"change": state}, q, "context_entry", items, contextPerRequest)
	if err != nil {
		return nil, fmt.Errorf("asking Jev which context a review needs: %w", err)
	}
	return out, nil
}

// clipRunes cuts s to at most n bytes without splitting a character.
func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
