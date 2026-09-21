package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
)

// The turn loop every model call of a review runs.
//
// A pass is a short conversation. Each turn the model makes calls; each call
// is checked, recorded when it is good and answered with what is wrong when it
// is not; and the loop goes on until the model calls done, the output budget
// is spent, or the turn cap is reached. What was recorded is kept however it
// ends. A pass that stops early says why, so a review cut short never reads as
// a review that had nothing more to say.
//
// Every turn resends the conversation so far, with a cache breakpoint on the
// prompt and one on the newest turn, so a turn pays the cache rate for all of
// it but what the previous turn added.
//
// A pass never costs more than it was priced at. The tripwire prices each pass
// as one request at its full output allowance, before anything is sent. A loop
// also resends its history each turn, and pricing every turn's worst case up
// front would refuse reviews that run today, so the allowance is enforced as
// the loop runs instead: each turn's output cap is what the pass's ceiling can
// still pay for after what it has spent and what this turn's input will cost,
// and a pass with no room left for a useful turn stops there.

// DefaultCallTurns bounds one pass's turns. The output budget is the real
// limit; this stops a model that keeps making calls without calling done.
//
// 12 was the number for a pass that could only record. A pass that can look
// things up spends turns before it writes anything, and it spends them at the
// front: on this repository's own pull request #83, a judging pass with the
// lookups on used all twelve on grep and read_lines, was cut off mid-search,
// and filed no comments at all. The cap has to leave room to look and then
// still write, so it is set past what that pass reached rather than at it.
//
// The output budget still governs the money. A turn that has no allowance
// left stops the pass whatever this says, so raising it buys turns for a pass
// that has room and changes nothing for one that does not.
const DefaultCallTurns = 30

// minTurnTokens is the smallest output allowance a turn is sent with. Below
// it a turn cannot make one useful call, so the loop stops on the budget
// rather than spend a request on a truncated one.
const minTurnTokens int64 = 1024

// Why a pass stopped before it called done.
const (
	StoppedTurnCap = "turn cap"
	StoppedBudget  = "output budget"
	StoppedNoCalls = "a reply with no calls"
	StoppedCostCap = "cost cap"
)

// toolCall is one call from a reply, on either wire.
type toolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// callResult is the answer to one call.
type callResult struct {
	id      string
	content string
	isError bool
}

// turnReply is one turn's answer, read off either wire.
type turnReply struct {
	calls      []toolCall
	text       string
	thinking   string
	stopReason string
	truncated  bool
	refused    bool
	detail     string
	usage      Usage
	model      string
}

// conversation is one wire's side of the loop: send the conversation as it
// stands, then extend it with the reply and the answers to its calls.
type conversation interface {
	send(ctx context.Context, maxTokens int64) (turnReply, error)
	answer(reply turnReply, results []callResult)
	nudge(reply turnReply, text string)
}

// nudgeText is sent once when a reply makes no calls at all, which a model
// that was not pinned to the tools does when it answers in prose.
const nudgeText = "Answer with the tool calls this pass asks for, not in prose. Call done when they are all made."

// converse runs one pass to its end and returns what it recorded as one
// completion, the shape the rest of the review reads.
func converse(ctx context.Context, opts Options, res *Result, conv conversation) (completion, error) {
	stage := res.stage()
	col := newCollector(stage, res.expect, res.deferred, opts.Look, res.recaps())
	var c completion
	var total Usage
	var thinking strings.Builder
	nudged := false
	gov := newGovernor(opts, res)
	for turn := 1; ; turn++ {
		if turn > opts.callTurns() {
			c.stopped = StoppedTurnCap
			break
		}
		remaining, why := gov.allowance(turn, opts.MaxTokens-total.OutputTokens, total)
		if remaining < minTurnTokens {
			if turn > 1 {
				c.stopped = why
				break
			}
			remaining = min(minTurnTokens, opts.MaxTokens)
		}
		r, err := conv.send(ctx, remaining)
		gov.sent(r)
		total = total.plus(r.usage)
		c.turns = turn
		if r.model != "" {
			c.model = r.model
		}
		if strings.TrimSpace(r.thinking) != "" {
			// Labelled by turn, so a reader can see where in the pass the
			// reasoning happened: all before the first call, or between them.
			if thinking.Len() > 0 {
				thinking.WriteString("\n\n")
			}
			fmt.Fprintf(&thinking, "[turn %d]\n%s", turn, r.thinking)
		}
		c.stopReason = r.stopReason
		if err != nil {
			if col.any() {
				// A turn that broke after earlier turns recorded something
				// keeps what they recorded. The error is the reason it
				// stopped.
				c.stopped = "error: " + err.Error()
				break
			}
			c.usage, c.thinking = total, thinking.String()
			return c, err
		}
		if r.refused {
			if col.any() {
				c.stopped = "refused: " + r.detail
				break
			}
			c.refused, c.detail = true, r.detail
			break
		}
		results, done, rejected := col.take(r.calls)
		if opts.Debug != nil {
			opts.Debug(fmt.Sprintf("%s turn %d: %d call(s), %d output token(s) of which %d thinking, %d thinking char(s) shown",
				stage, turn, len(r.calls), r.usage.OutputTokens, r.usage.ThinkingTokens, len(r.thinking)))
		}
		if opts.Debug != nil {
			// Which calls were sent back and why, with the input as sent: the
			// rate of rejected calls is the number that says whether unchecked
			// tool input holds up, and the reasons are what would change it.
			byID := map[string]toolCall{}
			for _, call := range r.calls {
				byID[call.ID] = call
			}
			for _, call := range r.calls {
				// Without this, the only record of what a --look pass
				// actually looked up is the model's own narration of it: a
				// lookup records nothing on its own. See collector.looked.
				switch call.Name {
				case CallContext:
					opts.Debug(fmt.Sprintf("%s turn %d: get_context %s", stage, turn, string(call.Input)))
				default:
					if isLookCall(call.Name) {
						opts.Debug(fmt.Sprintf("%s turn %d: %s %s", stage, turn, call.Name, string(call.Input)))
					}
				}
			}
			for _, res := range results {
				if res.isError {
					call := byID[res.id]
					opts.Debug(fmt.Sprintf("%s turn %d: sent back %s: %s\n  input: %s",
						stage, turn, call.Name, res.content, truncateForDebug(string(call.Input))))
				}
			}
		}
		if opts.Progress != nil && len(r.calls) > 0 {
			line := fmt.Sprintf("%s turn %d: %d call(s), %s so far", stage, turn, len(r.calls), col.progress())
			if rejected > 0 {
				line += fmt.Sprintf(", %d rejected this turn", rejected)
			}
			opts.Progress(line)
		}
		if r.truncated {
			if col.any() {
				c.stopped = StoppedBudget
			} else {
				c.truncated = true
				c.text = r.text
			}
			break
		}
		if done {
			break
		}
		if rejected == 0 && col.complete() {
			// Everything the pass exists to record is in, so another request
			// would only resend the conversation to collect done.
			if opts.Debug != nil {
				opts.Debug(fmt.Sprintf("%s turn %d: complete without done", stage, turn))
			}
			break
		}
		if len(r.calls) == 0 {
			if !col.any() && !nudged {
				nudged = true
				conv.nudge(r, nudgeText)
				continue
			}
			if !col.any() {
				// Nothing recorded and prose twice: hand the prose to the
				// parser, which reads a review out of a reply nothing
				// constrained and says what is wrong when it cannot.
				c.text = r.text
				c.usage, c.thinking, c.rejected, c.fetched, c.looked = total, thinking.String(), col.rejected, col.fetched, col.looked
				c.lookups, c.refusals = col.lookups, col.refusals
				return c, nil
			}
			c.stopped = StoppedNoCalls
			break
		}
		conv.answer(r, results)
		gov.answered(results)
	}
	c.usage, c.thinking, c.rejected, c.fetched, c.looked = total, thinking.String(), col.rejected, col.fetched, col.looked
	c.lookups, c.refusals = col.lookups, col.refusals
	if !c.refused && !c.truncated {
		c.text, c.fromTool = col.body(), true
	}
	return c, nil
}

// callTurns is the turn cap for one pass.
func (o Options) callTurns() int {
	if o.CallTurns > 0 {
		return o.CallTurns
	}
	return DefaultCallTurns
}

// plus adds two turns' usage.
func (u Usage) plus(v Usage) Usage {
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
	u.CacheReadTokens += v.CacheReadTokens
	u.CacheWriteTokens += v.CacheWriteTokens
	u.ThinkingTokens += v.ThinkingTokens
	return u
}

// foldCalls adds another pass's turns, rejections and early stops to this
// result, for a result that stands for several passes: samples, cohorts, the
// describing call and the ruling.
func (r *Result) foldCalls(other *Result) {
	if r == nil || other == nil || r == other {
		return
	}
	r.CallTurns += other.CallTurns
	r.Rejected += other.Rejected
	r.Fetched += other.Fetched
	r.Looked += other.Looked
	r.Lookups = append(r.Lookups, other.Lookups...)
	r.Refusals = append(r.Refusals, other.Refusals...)
	r.Stopped = append(r.Stopped, other.Stopped...)
	r.Thinking = append(r.Thinking, other.Thinking...)
}

// governor keeps a pass inside the cost it was priced at.
type governor struct {
	model   string
	ceiling float64
	on      bool
	cached  bool
	prompt  int64
	// history is what the conversation carries past the prompt, and fresh
	// the part of it the previous turn added, which a cached turn writes
	// rather than reads.
	history int64
	fresh   int64
}

func newGovernor(opts Options, res *Result) *governor {
	return &governor{
		model:   opts.Model,
		ceiling: res.CostCeilingUSD,
		on:      res.CostKnown && res.CostCeilingUSD > 0,
		cached:  opts.cacheOn(),
		prompt:  int64(res.InputEstimate),
	}
}

// allowance is this turn's output cap: what is left of the token budget, cut
// to what the pass's price can still pay for. why names which of the two
// bound it, for a pass that has to stop.
func (g *governor) allowance(turn int, tokensLeft int64, spent Usage) (int64, string) {
	if !g.on {
		return tokensLeft, StoppedBudget
	}
	next := Usage{InputTokens: g.prompt + g.history}
	if turn > 1 && g.cached {
		next = Usage{CacheReadTokens: g.prompt + g.history - g.fresh, CacheWriteTokens: g.fresh}
	}
	in, _ := next.Cost(g.model)
	paid, _ := spent.Cost(g.model)
	perToken, _ := Usage{OutputTokens: 1_000_000}.Cost(g.model)
	if perToken <= 0 {
		return tokensLeft, StoppedBudget
	}
	// Rounded, not truncated: a ceiling priced at exactly this allowance
	// comes back a hair under it in floating point, and a token short of the
	// cap is a cap the caller did not set.
	afford := int64((g.ceiling-paid-in)/(perToken/1_000_000) + 0.5)
	if afford < tokensLeft {
		return max(afford, 0), StoppedCostCap
	}
	return tokensLeft, StoppedBudget
}

func (g *governor) sent(r turnReply) {
	g.fresh = r.usage.OutputTokens
	g.history += r.usage.OutputTokens
}

func (g *governor) answered(results []callResult) {
	var n int
	for _, r := range results {
		n += envelope.EstimateTokens(r.content) + 8
	}
	g.fresh += int64(n)
	g.history += int64(n)
}

func truncateForDebug(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}
