package history

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/chrisophus/redline/internal/review"
)

// The words a model answers with. They are whole answers, not prose, so that
// a reply can be read without a schema on any endpoint.
const (
	SaidSingle = "single"
	SaidSplit  = "split"
	SaidLook   = "look"
	SaidStop   = "stop"
)

// Budgets for the diff a split question carries. The question is whether the
// files depend on each other, which the first part of each file's diff
// usually shows, and a question that carried a whole large change would cost
// as much as the review it is deciding about.
const (
	diffPerFile = 3000
	diffTotal   = 40000
	// askWorkers is how many questions are in flight at once.
	askWorkers = 4
)

// Question is one yes/no question about a past review. It is kept in parts
// because the endpoints take it differently: a chat model gets one prompt and
// answers with a word, and a decision model gets the state and the question
// apart and answers with a probability.
type Question struct {
	// System says what kind of decision this is, for an endpoint that takes
	// a system prompt.
	System string
	// State is everything the answer depends on, and Ask the question about
	// it, written so that yes and no are whole answers.
	State string
	Ask   string
	// Yes and No are the words the two answers are recorded as.
	Yes, No string
}

// Answer is what a model said to one question.
type Answer struct {
	// Said is the question's Yes or No word.
	Said string
	// P is the probability of yes, and HasP whether the endpoint gave one. A
	// chat model answers with a word and gives none.
	P     float64
	HasP  bool
	Usage review.Usage
}

// Asker puts one question to a model.
type Asker interface {
	Ask(ctx context.Context, q Question) (Answer, error)
}

// Asked is what the model said about one review.
type Asked struct {
	// Shape is single or split, and empty when the review's change is not
	// known or the model did not answer.
	Shape string `json:"shape,omitempty"`
	// SplitP is the probability it gave to split, nil from an endpoint that
	// gives none.
	SplitP *float64 `json:"splitP,omitempty"`
}

// Replay is one model's answers over every review, and what asking cost.
type Replay struct {
	Model string `json:"model"`
	// Asks is how many questions were answered, Failed how many were sent
	// and came back with an error or no usable word, and Skipped how many
	// were never sent because of the limit on questions.
	Asks    int          `json:"asks"`
	Failed  int          `json:"failed,omitempty"`
	Skipped int          `json:"skipped,omitempty"`
	Usage   review.Usage `json:"usage"`
	CostUSD float64      `json:"costUSD"`
	// CostKnown is false for a model with no entry in the price table.
	CostKnown bool `json:"costKnown"`
	// FirstError is the first failure's text, which is usually the reason
	// for all of them.
	FirstError string `json:"firstError,omitempty"`
}

type job struct {
	run    int
	lookup int // -1 for the split question
	q      Question
}

// Ask puts each past review's choices to a model: whether the change should
// have been split, and for each lookup whether it was worth making. The
// answers are written onto the runs and the totals onto rep.Replay. Split
// questions go first, so a limit on questions cuts lookups before it cuts
// reviews. progress, when not nil, is called after each answer.
//
// It returns an error only when every question failed, which is a wrong key
// or endpoint and not something a partial report would explain.
func (rep *Report) Ask(ctx context.Context, a Asker, model string, limit int, progress func(done, total int)) error {
	var jobs []job
	for i, r := range rep.Runs {
		if r.SizeKnown && r.set != nil && r.Files > 0 {
			jobs = append(jobs, job{run: i, lookup: -1, q: shapeQuestion(r)})
		}
	}
	for i, r := range rep.Runs {
		for j := range r.Each {
			jobs = append(jobs, job{run: i, lookup: j, q: lookQuestion(r, j)})
		}
	}
	rp := &Replay{Model: model}
	rep.Replay = rp
	if limit > 0 && len(jobs) > limit {
		rp.Skipped = len(jobs) - limit
		jobs = jobs[:limit]
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	next := make(chan job)
	done := 0
	for range askWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range next {
				ans, err := a.Ask(ctx, j.q)
				mu.Lock()
				rp.Usage = sum(rp.Usage, ans.Usage)
				if err != nil {
					rp.Failed++
					if rp.FirstError == "" {
						rp.FirstError = err.Error()
					}
				} else {
					rp.Asks++
					rep.record(j, ans)
				}
				done++
				if progress != nil {
					progress(done, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		next <- j
	}
	close(next)
	wg.Wait()

	rp.CostUSD, rp.CostKnown = askCost(model, rp.Usage)
	if rp.Asks == 0 && rp.Failed > 0 {
		return fmt.Errorf("no question was answered: %s", rp.FirstError)
	}
	return nil
}

func (rep *Report) record(j job, ans Answer) {
	var p *float64
	if ans.HasP {
		p = &ans.P
	}
	r := &rep.Runs[j.run]
	if j.lookup < 0 {
		r.Asked = &Asked{Shape: ans.Said, SplitP: p}
		return
	}
	r.Each[j.lookup].Said, r.Each[j.lookup].StopP = ans.Said, p
}

func sum(a, b review.Usage) review.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	a.ThinkingTokens += b.ThinkingTokens
	return a
}

const shapeSystem = "You decide how a code change is sent to a model that reviews it. " +
	"Reply with one word and nothing else."

const lookSystem = "You decide whether a model that is reviewing a code change should keep looking things up. " +
	"Reply with one word and nothing else."

// shapeQuestion carries what was known before the review ran: the files,
// their sizes and the start of each diff. Nothing about how the review went
// is in it, since the point is to see what the model would have chosen
// beforehand.
func shapeQuestion(r Run) Question {
	var b strings.Builder
	b.WriteString("A code change is about to be reviewed by a model. It can go in a single call that reads " +
		"every file together, or be split into groups of files that are each judged in their own call.\n\n" +
		"A single call can see a defect that needs two files to notice. A split gives each group more " +
		"attention and keeps a large change inside the limits on prompt size and output.\n\n")
	b.WriteString(fileList(r))
	b.WriteString("\nThe start of each file's diff:\n")
	left := diffTotal
	for _, f := range r.set.Files {
		if left <= 0 {
			b.WriteString("\n(the remaining diffs are left out for length)\n")
			break
		}
		d := f.Diff
		if len(d) > diffPerFile {
			d = d[:diffPerFile] + "\n(cut)"
		}
		if len(d) > left {
			d = d[:left] + "\n(cut)"
		}
		left -= len(d)
		fmt.Fprintf(&b, "\n--- %s\n%s\n", f.Path, d)
	}
	return Question{System: shapeSystem, State: b.String(), Yes: SaidSplit, No: SaidSingle,
		Ask: "Should this change be split into groups of files that are judged in separate calls, " +
			"instead of being reviewed in a single call?"}
}

// lookQuestion carries what the reviewer had when it made lookup j: the
// change, the lookups before it with how much each returned, and the one it
// is about to make. What the earlier lookups returned is not saved, only its
// size.
func lookQuestion(r Run, j int) Question {
	var b strings.Builder
	b.WriteString("A model is reviewing a code change and can look things up in the repository while it " +
		"works. Each lookup costs a turn of the review.\n\n")
	if r.set != nil {
		b.WriteString(fileList(r))
		b.WriteString("\n")
	}
	if j == 0 {
		b.WriteString("It has made no lookups yet.\n")
	} else {
		b.WriteString("Lookups it has made so far, in order, with what each returned:\n")
		for i, l := range r.Each[:j] {
			got := comma(l.Bytes) + " bytes"
			if l.Empty {
				got = "nothing found"
			}
			fmt.Fprintf(&b, "  %d. %s %s -> %s\n", i+1, l.Tool, l.Input, got)
		}
	}
	fmt.Fprintf(&b, "\nThe lookup it wants to make next:\n  %s %s\n", r.Each[j].Tool, r.Each[j].Input)
	return Question{System: lookSystem, State: b.String(), Yes: SaidStop, No: SaidLook,
		Ask: "Should the reviewer stop looking things up now and write up what it has, because that " +
			"next lookup is unlikely to tell it anything it needs?"}
}

// prompt is the question as one text, for an endpoint that answers in words.
func (q Question) prompt() string {
	return fmt.Sprintf("%s\n%s\n\nAnswer `%s` for yes or `%s` for no.\n", q.State, q.Ask, q.Yes, q.No)
}

func fileList(r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Files changed (%s, %s added, %s removed):\n",
		plural(r.Files, "file"), plural(r.Added, "line"), plural(r.Removed, "line"))
	for _, f := range r.set.Files {
		fmt.Fprintf(&b, "  %s  +%d -%d\n", f.Path, f.Added, f.Removed)
	}
	return b.String()
}

// errNoChoice is a reply that named none of the offered words, or more than
// one of them.
var errNoChoice = errors.New("the reply was not one of the offered words")

// choose reads the model's one-word answer. A reply that is exactly one of
// the choices, give or take case, quotes and a full stop, is that choice. A
// longer reply counts only when it names exactly one of them.
func choose(reply string, choices []string) (string, error) {
	clean := strings.ToLower(strings.Trim(strings.TrimSpace(reply), "`'\".!* \n"))
	for _, c := range choices {
		if clean == c {
			return c, nil
		}
	}
	var found []string
	for _, w := range strings.FieldsFunc(clean, func(r rune) bool { return r < 'a' || r > 'z' }) {
		for _, c := range choices {
			if w == c {
				found = append(found, c)
			}
		}
	}
	sort.Strings(found)
	if len(found) == 0 || found[0] != found[len(found)-1] {
		return "", fmt.Errorf("%w: %q", errNoChoice, truncate(reply, 80))
	}
	return found[0], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
