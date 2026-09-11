// Package scout fills a context envelope with a model instead of a parser.
//
// It is a context provider like any other: Redline runs it as a subprocess in
// the tree under review and reads one envelope from its stdout. What is
// different is how the envelope gets filled. gorefactor resolves every caller
// of every changed symbol because it cannot know which ones matter; the scout
// reads the diff, forms a view about what this particular change needs, and
// fetches that. A deleted guard pulls history. A changed signature pulls
// callers. A struct whose fields moved pulls the migration that writes the
// row.
//
// The envelope contract anticipated this: it says the split is
// language-specific versus language-agnostic and not deterministic versus
// probabilistic, "because a provider is free to use a model of its own to
// fill an envelope". This is that provider.
//
// One rule holds the whole design up. The model picks and this program
// copies: an expansion's content is read from the tree by record.go, never
// written by the model, so a cheap model's recollection of a function cannot
// reach the reviewer looking like source. The only model-written text in the
// envelope is the notes, which say what it looked for and could not find.
//
// The cost of that is reproducibility. gorefactor writes the same envelope
// for a revision every time; the scout does not, so the model and the effort
// are what provider.version carries, and a fixture that freezes a scouted
// session freezes one particular run of it.
package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/review"
)

// DefaultModel is the cheap half of the two-model split. The scout fetches
// and does not judge, so it is bought at the rate of the thing it is: a
// search, run several times, whose output another model reads.
const DefaultModel = "claude-sonnet-5"

// DefaultEffort keeps the scout's own thinking short. It is deciding what to
// look at, not what to say about it.
const DefaultEffort = "low"

// DefaultMaxCostUSD is the scout's whole allowance for one change. It is a
// governor rather than a tripwire, unlike the reviewer's: when the scout runs
// out it stops early and says so in a note, and the reviewer still gets the
// diff and everything recorded so far. There is no case where hitting this
// means no review.
const DefaultMaxCostUSD = 0.25

// DefaultMaxTurns bounds the loop. Each turn resends the conversation, so
// turns are the thing that costs money as the run goes on.
const DefaultMaxTurns = 8

// DefaultMaxTokens is one turn's output cap. A turn is a handful of tool
// calls, not an essay.
const DefaultMaxTokens = 8192

// runTimeout keeps the scout inside the five minutes Redline gives a
// provider, with room for the envelope to be written.
const runTimeout = 4 * time.Minute

// Options configure one scout run.
type Options struct {
	Root    string
	Changed []string
	BaseSHA string
	// Diff is the change as the reviewer will see it. It is the scout's whole
	// brief: everything else it wants, it fetches.
	Diff string
	// Intent is what the author said the change does: commit messages, and
	// the pull request's title and body when there is one. It steers an
	// exploring search, which has to work out for itself what to look at,
	// and answerBrief says why a search given questions is not sent it.
	Intent string
	// Generated maps a changed path to why it is machine output, for the ones
	// that are. Their diffs are held back, and the manifest says so, which is
	// the answer to "should a reviewer read this" that the envelope carries.
	Generated map[string]string
	// Graph is the path to a Graphify graph, when the repository has one. It
	// turns on the two cross-language tools.
	Graph string
	// Covered are the roles another provider already resolves, and the files
	// it resolves them for. The scout is told, and record refuses them.
	//
	// This is the difference between a provider that adds something and one
	// that pays a model to redo what a type checker already did for free.
	// gorefactor resolves the whole vocabulary exhaustively on Go: on this
	// repository's own change it returned 327 enclosing declarations, 29
	// callers, 19 types, 12 siblings and 44 history entries. A scout spending
	// turns on any of those is spending money to arrive second.
	Covered []envelope.Role
	// CoveredScope are the globs Covered applies to. Empty means everywhere.
	CoveredScope []string

	// Questions turn the scout from an explorer into a checker. When they are
	// set it stops guessing what a reviewer will need and answers what one
	// actually asked, which is the better job: the guess is made before
	// anything has been reviewed, and these arrive after.
	//
	// Everything else is shared. Same loop, same tools, same governor, same
	// rule that the model records a location and this program reads the bytes.
	Questions []Question

	Model      string
	Effort     string
	MaxTurns   int
	MaxTokens  int64
	MaxCostUSD float64
	// API is the wire the scout's own calls go over: "anthropic" (default) or
	// "openai". It is set to whatever wire the review ran on, so the lookups a
	// review asked for are answered by the same provider that produced them.
	API     string
	APIKey  string
	BaseURL string
	// APIUser names the caller to an OpenAI gateway that meters by user. It
	// rides beside the key the same way the reviewer's own OpenAI wire sends
	// it, and the Anthropic wire ignores it.
	APIUser string

	Limits Limits

	// Progress is called once per turn with a short heartbeat, for a loop that
	// would otherwise print nothing for minutes. Debug is called with the
	// under-the-covers detail: each tool call and what it returned. Either may
	// be nil, and nothing depends on either being called.
	Progress func(string)
	Debug    func(string)
	// Capture, when set, is handed the whole conversation once the loop ends:
	// the system prompt, the brief, and every turn's tool calls and results,
	// which is everything the scout sent and got back.
	Capture func(name string, data []byte)
}

func (o Options) withDefaults() Options {
	if o.API == "" {
		o.API = review.APIAnthropic
	}
	if o.Model == "" {
		o.Model = DefaultModel
		if o.API == review.APIOpenAI {
			// The scout is the cheap half of the split on either wire. gpt-5
			// is the OpenAI default the reviewer uses, and the scout follows
			// it rather than defaulting to an Anthropic model on an OpenAI
			// endpoint, which would 404.
			o.Model = review.DefaultOpenAIModel
		}
	}
	if o.Effort == "" {
		o.Effort = DefaultEffort
	}
	if o.MaxTurns <= 0 {
		o.MaxTurns = DefaultMaxTurns
	}
	if o.MaxTokens <= 0 {
		o.MaxTokens = DefaultMaxTokens
	}
	if o.MaxCostUSD <= 0 {
		o.MaxCostUSD = DefaultMaxCostUSD
	}
	return o
}

// Spend is what one run cost, for the log line and the ledger.
type Spend struct {
	Usage     review.Usage `json:"usage"`
	CostUSD   float64      `json:"costUSD"`
	CostKnown bool         `json:"costKnown"`
	Turns     int          `json:"turns"`
	Records   int          `json:"records"`
	CapHit    bool         `json:"capHit"`
}

// Run drives the loop and returns the envelope it filled.
//
// A failure to reach the model is returned rather than swallowed: Redline
// records a provider that did not run, which is not the same thing as one
// that found nothing. A failure partway through is not, because the records
// already made are worth sending and the note says the run was cut short.
func Run(ctx context.Context, opts Options) (*envelope.Envelope, Spend, error) {
	opts = opts.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	ts := newToolset(opts.Root, opts.Graph, opts.Limits)
	ts.res.covered, ts.res.coveredScope = opts.Covered, opts.CoveredScope
	ts.debug = opts.Debug
	ts.answering = len(opts.Questions) > 0

	// Same loop, same tools, same governor on either wire. Only the transport
	// differs: the Anthropic SDK on one, plain chat-completions with function
	// calling on the other.
	var (
		spend Spend
		err   error
	)
	if opts.API == review.APIOpenAI {
		spend, err = driveOpenAI(ctx, opts, ts)
	} else {
		spend, err = driveAnthropic(ctx, opts, ts)
	}
	if err != nil {
		// Turn-zero failure: nothing was fetched and nothing is known. The
		// caller turns this into an absent provider, which is the honest
		// report and not the same as one that found nothing.
		return nil, spend, err
	}
	spend.Records = len(ts.records)
	env := build(opts, ts)
	return env, spend, nil
}

// driveAnthropic runs the tool loop over the Anthropic Messages API.
func driveAnthropic(ctx context.Context, opts Options, ts *toolset) (Spend, error) {
	var clientOpts []option.RequestOption
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(opts.BaseURL))
	}
	client := anthropic.NewClient(clientOpts...)

	// Two cache breakpoints, on the system prompt and on the brief. Every
	// turn resends both, and they are the bulk of a request: the diff and the
	// repository's rules run to thousands of tokens against a few hundred of
	// tool calls. The breakpoint on the system block also covers the tools
	// ahead of it. Under the cap this is the difference between three turns
	// and eight, because a cached read is a tenth of the price and the
	// governor sees that in the usage it is handed.
	brief := anthropic.NewTextBlock(briefFor(opts))
	brief.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(opts.Model),
		MaxTokens: opts.MaxTokens,
		System: []anthropic.TextBlockParam{{
			Text:         promptFor(opts, ts.Names()),
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(brief)},
		Tools:        ts.params(),
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(opts.Effort)},
	}

	var spend Spend
	// cachedLast is what the previous turn read from the cache, which is the
	// governor's evidence that the next turn will read it again.
	var cachedLast int64
	for turn := range opts.MaxTurns {
		// The last turn of the budget files rather than searches; see the
		// OpenAI loop, which does the same for the same reason.
		if turn > 0 && turn == opts.MaxTurns-1 && !ts.done {
			ts.closing = true
			params.Tools = ts.params()
			// The prompt names the tools, so it changes here, and with it
			// the cached prefix. One turn at the full rate, once.
			params.System = []anthropic.TextBlockParam{{
				Text:         promptFor(opts, ts.Names()),
				CacheControl: anthropic.NewCacheControlEphemeralParam(),
			}}
			params.Messages = append(params.Messages,
				anthropic.NewUserMessage(anthropic.NewTextBlock(closingFor(opts))))
			ts.notes = append(ts.notes, fmt.Sprintf(
				"the search for context stopped at its %d-turn limit; there may be context it had not reached", opts.MaxTurns))
			cachedLast = 0
		}
		if stop, reason := overBudget(opts, spend, params, cachedLast); stop {
			spend.CapHit = true
			ts.notes = append(ts.notes, reason)
			break
		}
		msg, err := client.Messages.New(ctx, params)
		if err != nil {
			if turn == 0 {
				return spend, fmt.Errorf("%s: %w", opts.Model, err)
			}
			ts.notes = append(ts.notes, fmt.Sprintf(
				"the search for context stopped early after %d turn(s): %v; what is below is what it had found by then", turn, err))
			break
		}
		spend.Turns++
		spend.Usage.InputTokens += msg.Usage.InputTokens
		spend.Usage.OutputTokens += msg.Usage.OutputTokens
		spend.Usage.CacheReadTokens += msg.Usage.CacheReadInputTokens
		spend.Usage.CacheWriteTokens += msg.Usage.CacheCreationInputTokens
		cachedLast = msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
		// Priced every turn, not once at the end: overBudget adds this to the
		// next turn's ceiling, and a total that is still zero while the loop
		// runs makes the governor a per-turn check that never accumulates.
		spend.CostUSD, spend.CostKnown = spend.Usage.Cost(opts.Model)
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("scout turn %d: %d record(s), %s so far",
				spend.Turns, len(ts.records), review.FormatCost(spend.CostUSD, spend.CostKnown)))
		}

		// ToParam carries the assistant turn back unchanged, thinking blocks
		// included, which is what a tool loop on a thinking model requires.
		params.Messages = append(params.Messages, msg.ToParam())

		results := runTools(ts, msg)
		if ts.done || len(results) == 0 {
			if !ts.done {
				ts.notes = append(ts.notes, "the search for context ended without a summary of what it could not find")
			}
			break
		}
		params.Messages = append(params.Messages, anthropic.NewUserMessage(results...))
	}
	spend.CostUSD, spend.CostKnown = spend.Usage.Cost(opts.Model)
	if opts.Capture != nil {
		b, _ := json.MarshalIndent(map[string]any{"system": params.System, "messages": params.Messages}, "", "  ")
		opts.Capture("scout.transcript.json", b)
	}
	return spend, nil
}

// runTools executes every tool call in one assistant turn and returns the
// results as one user message's worth of blocks. All of them go back in a
// single message: splitting tool results across messages teaches the model to
// stop calling tools in parallel.
func runTools(ts *toolset, msg *anthropic.Message) []anthropic.ContentBlockParamUnion {
	var results []anthropic.ContentBlockParamUnion
	for _, block := range msg.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		out, failed := ts.dispatch(use.Name, json.RawMessage(use.Input))
		results = append(results, anthropic.NewToolResultBlock(use.ID, out, failed))
		if ts.done {
			// done ends the run, but every call in this turn still needs a
			// result or the conversation is malformed for a retry.
			continue
		}
	}
	return results
}

// overBudget stops the loop before a turn that would take the run past its
// allowance. It prices the turn about to be sent, which is what a governor
// has to do: knowing afterwards that a turn was too expensive is knowing it
// too late.
//
// cachedLast is what the previous turn read from or wrote to the prompt
// cache. When it is more than nothing, the prefix under the breakpoints is
// priced at the cached rate, because that is what the wire will charge for
// it; a governor that prices a cached brief at the full rate stops a run
// three turns before its money is gone. It is evidence rather than
// assumption: a prefix too short to cache reports no cached tokens, and the
// full rate stands.
func overBudget(opts Options, spend Spend, params anthropic.MessageNewParams, cachedLast int64) (bool, string) {
	next := estimateInput(params)
	if cachedLast > 0 {
		next -= cacheDiscount(estimatePrefix(params))
	}
	ceiling, ok := review.CeilingCost(opts.Model, next, opts.MaxTokens)
	if !ok {
		// An unpriced model cannot be governed by cost. Turns still bound it.
		return false, ""
	}
	if spend.CostUSD+ceiling <= opts.MaxCostUSD {
		return false, ""
	}
	return true, fmt.Sprintf(
		"the search for context stopped at its cost cap of %s after %d turn(s); there may be context it had not reached",
		review.FormatCost(opts.MaxCostUSD, true), spend.Turns)
}

// cacheDiscount is how much less a cached prefix of n tokens costs than an
// uncached one, in tokens at the full rate. A cached read is a tenth of the
// input price.
func cacheDiscount(prefix int) int {
	return prefix * 9 / 10
}

// estimatePrefix sizes the part of the request under the cache breakpoints:
// the system prompt and the brief.
func estimatePrefix(params anthropic.MessageNewParams) int {
	n := 0
	for _, s := range params.System {
		n += envelope.EstimateTokens(s.Text)
	}
	if len(params.Messages) > 0 {
		for _, block := range params.Messages[0].Content {
			if t := block.OfText; t != nil {
				n += envelope.EstimateTokens(t.Text)
			}
		}
	}
	return n
}

// estimateInput sizes the next request from the conversation so far. It is
// the same crude character-count estimate Redline budgets the review with,
// and it is used here for the same reason: an exact count means another API
// call, and a governor that costs a call to consult is a worse governor.
func estimateInput(params anthropic.MessageNewParams) int {
	n := 0
	for _, s := range params.System {
		n += envelope.EstimateTokens(s.Text)
	}
	for _, m := range params.Messages {
		for _, block := range m.Content {
			if t := block.OfText; t != nil {
				n += envelope.EstimateTokens(t.Text)
			}
			if tr := block.OfToolResult; tr != nil {
				for _, c := range tr.Content {
					if c.OfText != nil {
						n += envelope.EstimateTokens(c.OfText.Text)
					}
				}
			}
			if tu := block.OfToolUse; tu != nil {
				if raw, err := json.Marshal(tu.Input); err == nil {
					n += envelope.EstimateTokens(string(raw))
				}
			}
		}
	}
	return n
}

// build assembles the envelope from what the run collected.
func build(opts Options, ts *toolset) *envelope.Envelope {
	res := ts.res
	expansions := res.Expansions(ts.records)
	notes := append(append([]string{}, ts.notes...), res.Notes()...)
	if len(ts.records) == 0 {
		if len(opts.Questions) > 0 {
			// A different fact from an unproductive scout, and the ruling
			// stage acts on the difference: every finding it is about to rule
			// on was checked and nothing came back, so none of them is
			// verified by anything here.
			notes = append(notes, fmt.Sprintf(
				"none of the %d question(s) turned up anything to put in front of the ruling",
				len(opts.Questions)))
		} else {
			notes = append(notes, "the search found nothing worth putting in front of the reviewer beyond the diff itself")
		}
	}
	return &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Provider: envelope.Provider{
			Name: "scout",
			// The model and the effort are what wrote this. A frozen fixture
			// has to be able to say what filled its envelope, and for a
			// provider that thinks, that is the model rather than a version.
			Version: opts.Model + "/" + opts.Effort,
		},
		BaseSHA:        opts.BaseSHA,
		Files:          manifest(opts.Changed, opts.Generated),
		Expansions:     expansions,
		PromptFragment: fragmentFor(opts),
		Notes:          notes,
	}
}
func manifest(changed []string, generated map[string]string) []envelope.File {
	out := make([]envelope.File, 0, len(changed))
	for _, p := range changed {
		f := envelope.File{Path: normPath(p), Class: classOf(p)}
		if generated[p] != "" {
			// Generated is the answer to "should a reviewer read this",
			// regardless of which side decided. Redline summarizes these
			// rather than dropping them, so the count still reaches the model.
			f.Generated = true
			f.Class = envelope.ClassGenerated
		}
		out = append(out, f)
	}
	return out
}

// classOf is the coarse classification a provider that speaks no language in
// particular can honestly make: by path, the same evidence the reader has.
func classOf(path string) envelope.Class {
	p := strings.ToLower(normPath(path))
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	switch {
	case strings.Contains(p, "/vendor/") || strings.HasPrefix(p, "vendor/") || strings.Contains(p, "node_modules/"):
		return envelope.ClassVendored
	case strings.HasSuffix(base, "_test.go") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasPrefix(base, "test_"):
		return envelope.ClassTest
	case strings.Contains(p, "migration") && strings.HasSuffix(base, ".sql"):
		return envelope.ClassMigration
	case base == "go.sum" || strings.HasSuffix(base, ".lock") || base == "package-lock.json":
		return envelope.ClassLockfile
	default:
		return envelope.ClassSource
	}
}
