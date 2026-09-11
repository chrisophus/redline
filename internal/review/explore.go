package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/chrisophus/redline/internal/envelope"
)

// Explore mode spends more to get a better review, up to a cap.
//
// The one-shot producer decides in advance what context is worth sending and
// sends all of it. This one sends a catalogue instead: every expansion the
// provider resolved, listed by role, symbol and location, with no content.
// The reviewer reads the diff, decides what it actually needs, and asks. It
// pays for what it asked for rather than for what was guessed.
//
// The corpus is the frozen envelope and nothing else. There is no filesystem
// and no network behind the tool, so a review is still a pure function of a
// saved session: the same session explored twice can see exactly the same
// material, which is what keeps the eval able to replay it.
//
// This costs more than one shot, and it is meant to. Every turn re-sends the
// conversation, so the price is the resend, and the cap is what makes that
// acceptable: spend is measured after each turn against a dollar limit, and
// the loop stops granting tools before it can exceed it. A task budget on top
// of that lets the model pace itself rather than be cut off mid-thought.

// fetchTool is the only tool. Deliberately singular: a reviewer that has to
// choose between six retrieval verbs spends turns choosing.
const fetchToolName = "fetch_context"

// DefaultTaskBudget paces the model. Its own accounting covers what it
// generates and what it reads back this turn, so it is not a dollar cap and
// the dollar cap is enforced here instead. The API's minimum is 20,000.
const DefaultTaskBudget int64 = 40000

// catalogue renders the expansions as a list the reviewer picks from. One
// line each, no content: the whole point is that the content costs nothing
// until it is asked for.
func catalogue(kept []envelope.Expansion) string {
	var b strings.Builder
	b.WriteString("## Context available on request\n\n")
	b.WriteString("Each line is a piece of context the provider resolved for this change. " +
		"None of it is below. Call " + fetchToolName + " with the ids you want and it will be " +
		"returned in full. Ask for what the diff makes you want to check, and nothing else: " +
		"reading everything costs more and reviews no better.\n\n")
	byRole := map[envelope.Role][]string{}
	for i, x := range kept {
		loc := x.File
		if x.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
		}
		line := fmt.Sprintf("  e%d  %s  %s", i, x.Symbol, loc)
		byRole[x.Role] = append(byRole[x.Role], line)
	}
	for _, r := range envelope.Roles() {
		lines := byRole[r]
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s (%d):\n", r, len(lines))
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// fetch returns the requested expansions' content, and says so when an id is
// not one it has.
func fetch(kept []envelope.Expansion, ids []string) string {
	var b strings.Builder
	seen := map[int]bool{}
	for _, id := range ids {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(id), "e"))
		if err != nil || n < 0 || n >= len(kept) {
			fmt.Fprintf(&b, "%s: no such id\n\n", id)
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		x := kept[n]
		loc := x.File
		if x.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
		}
		fmt.Fprintf(&b, "── e%d · %s · %s · %s ──\n%s\n\n", n, x.Role, x.Symbol, loc, x.Content)
	}
	if b.Len() == 0 {
		return "nothing was returned; check the ids against the catalogue"
	}
	return b.String()
}

func fetchToolParam() anthropic.BetaToolUnionParam {
	return anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
		Name: fetchToolName,
		Description: anthropic.String(
			"Return the full content of context entries listed in the catalogue. " +
				"Pass the ids exactly as they appear there, for example [\"e3\",\"e17\"]. " +
				"Ask for several at once rather than one per call."),
		InputSchema: anthropic.BetaToolInputSchemaParam{
			Properties: map[string]any{
				"ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Catalogue ids to return in full.",
				},
			},
			Required: []string{"ids"},
		},
	}}
}

// runExplore drives the tool loop. It returns when the model stops asking for
// context, when the dollar cap is reached, or when the turn limit is.
func runExplore(ctx context.Context, in Input, opts Options, res *Result) (*Result, error) {
	budget := envelope.FitAllFilter(in.Envelopes, opts.Ceiling, in.shownLines(), in.contextFilter())
	kept := budget.Kept
	res.Budget = budget
	res.Prompt = in.build(envelope.Budgeted{}) + catalogue(kept)

	var clientOpts []option.RequestOption
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	client := anthropic.NewClient(clientOpts...)

	msgs := []anthropic.BetaMessageParam{
		anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(res.Prompt)),
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaTaskBudgets2026_03_13},
		System: []anthropic.BetaTextBlockParam{{
			Text: systemPrompt + exploreAddendum + languageFragments(in.Envelopes),
		}},
		Tools: []anthropic.BetaToolUnionParam{fetchToolParam()},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Format:     anthropic.BetaJSONOutputFormatParam{Schema: outputSchema()},
			TaskBudget: anthropic.BetaTokenTaskBudgetParam{Total: opts.TaskBudget},
		},
	}
	if opts.Effort != "" {
		params.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(opts.Effort)
	}

	start := time.Now()
	for turn := 1; ; turn++ {
		params.Messages = msgs
		// Streamed for the same reason the one-shot call is: a non-streaming
		// request that may run past ten minutes is refused outright, and this
		// loop grows toward that limit rather than away from it, because
		// every turn resends the conversation. The blocking call failed on
		// turn one against a real change.
		stream := client.Beta.Messages.NewStreaming(ctx, params)
		var acc anthropic.BetaMessage
		var streamErr error
		// A turn of this loop is as silent as the one-shot call was, and for
		// longer: the per-turn line below cannot print until the turn ends.
		hb := newHeartbeat(opts, fmt.Sprintf("turn %d", turn))
		for stream.Next() {
			ev := stream.Current()
			if err := acc.Accumulate(ev); err != nil {
				streamErr = fmt.Errorf("accumulate (turn %d): %w", turn, err)
				break
			}
			hb.observe(ev.Delta.Type, ev.Delta.Text, ev.Delta.Thinking, ev.Delta.PartialJSON)
		}
		if streamErr == nil {
			if err := stream.Err(); err != nil {
				streamErr = fmt.Errorf("review call (turn %d): %w", turn, err)
			}
		}
		// Closed here rather than deferred: this loop opens a stream per
		// turn, and Next returning false does not close the response body,
		// so deferring would hold every turn's connection open until the
		// review finished and leak one per turn on the error paths.
		if err := stream.Close(); err != nil && streamErr == nil {
			streamErr = fmt.Errorf("review call (turn %d): closing the stream: %w", turn, err)
		}
		if streamErr != nil {
			return res, streamErr
		}
		msg := &acc
		res.Turns = turn
		res.Usage.InputTokens += msg.Usage.InputTokens
		res.Usage.OutputTokens += msg.Usage.OutputTokens
		res.Usage.CacheReadTokens += msg.Usage.CacheReadInputTokens
		res.Usage.CacheWriteTokens += msg.Usage.CacheCreationInputTokens
		res.CostUSD, res.CostKnown = res.Usage.Cost(opts.Model)
		res.Duration = time.Since(start)
		res.StopReason = string(msg.StopReason)
		if opts.Progress != nil {
			// Each turn is a minute or more and the loop prints nothing until
			// it is over, so a run that is working looks the same as one that
			// has hung.
			opts.Progress(fmt.Sprintf("turn %d: %s so far, %d context entr%s fetched",
				turn, FormatCost(res.CostUSD, res.CostKnown), res.Fetched,
				map[bool]string{true: "y", false: "ies"}[res.Fetched == 1]))
		}

		if msg.StopReason == anthropic.BetaStopReasonRefusal {
			return res, fmt.Errorf("the model declined this request (%s); no review was produced",
				msg.StopDetails.Category)
		}
		if msg.StopReason != anthropic.BetaStopReasonToolUse {
			body := betaTextOf(msg)
			if strings.TrimSpace(body) == "" {
				return res, fmt.Errorf("the model returned no review (stop reason %q)", msg.StopReason)
			}
			rev, perr := parseReview([]byte(body))
			if perr != nil {
				return res, perr
			}
			res.Review = *rev
			return res, nil
		}

		msgs = append(msgs, msg.ToParam())
		results, asked := toolResults(msg, kept)
		res.Fetched += asked

		// The cap is enforced here, because the API's task budget counts what
		// the model writes and reads this turn and not the history resent on
		// the next one, which is where the money actually goes.
		if capReached(res, opts, msg) {
			res.CapHit = true
			results = append(results, anthropic.NewBetaTextBlock(
				"The context budget for this review is spent. Do not call "+fetchToolName+
					" again. Write the review now from what you already have."))
			params.Tools = nil
		}
		msgs = append(msgs, anthropic.NewBetaUserMessage(results...))

		if turn >= opts.MaxTurns {
			params.Tools = nil
		}
	}
}

// exploreAddendum is the part of the instruction that only applies when the
// reviewer can ask for more.
const exploreAddendum = `

You can ask for context. Below the diff is a catalogue of what the provider
resolved for this change: enclosing declarations, callers, types, sibling
implementations, tests, and the history of the changed lines. None of it is in
front of you until you ask.

Read the diff first and form a question, then fetch what answers it. Fetching
everything is not thorough, it is expensive and it buries the thing that
mattered. History is usually the highest-value fetch on a change that removes
code, because the diff cannot tell you why the code was there.

When you have what you need, stop fetching and write the review.`

func toolResults(msg *anthropic.BetaMessage, kept []envelope.Expansion) ([]anthropic.BetaContentBlockParamUnion, int) {
	var out []anthropic.BetaContentBlockParamUnion
	var asked int
	for _, block := range msg.Content {
		use, ok := block.AsAny().(anthropic.BetaToolUseBlock)
		if !ok {
			continue
		}
		var input struct {
			IDs []string `json:"ids"`
		}
		if raw, err := json.Marshal(use.Input); err == nil {
			_ = json.Unmarshal(raw, &input)
		}
		asked += len(input.IDs)
		out = append(out, anthropic.NewBetaToolResultBlock(use.ID, fetch(kept, input.IDs), false))
	}
	return out, asked
}

// capReached reports whether another turn would risk exceeding the cap. The
// next turn resends everything so far, so the test is against what has been
// spent plus what resending it would cost again.
func capReached(res *Result, opts Options, msg *anthropic.BetaMessage) bool {
	if !res.CostKnown {
		return false
	}
	p, ok := LookupPricing(opts.Model)
	if !ok {
		return false
	}
	nextResend := float64(msg.Usage.InputTokens+msg.Usage.OutputTokens) / 1e6 * p.InPerM
	return res.CostUSD+nextResend >= opts.MaxCostUSD
}

func betaTextOf(msg *anthropic.BetaMessage) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
