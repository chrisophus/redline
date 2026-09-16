package review

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/chrisophus/redline/internal/envelope"
)

// The output contracts, as one tool array that is the same on every call.
//
// A response schema is not free to vary. It renders ahead of the system block,
// so it sits in front of anything a later call could read back, and changing
// it between two calls over the same prompt invalidates the whole prefix. That
// was measured rather than reasoned about: the ruling keeps the system block
// byte-identical and appends its instruction to the tail of the user turn,
// which should leave the prefix intact, and the wire still wrote 171,690 and
// 170,601 tokens and read nothing either time.
//
// It cannot be moved behind the prompt. `output_config.format` is a top-level
// request parameter with no position to set, and a decoding constraint has to
// be compiled before the first token is sampled, so there is no later point
// for it to take effect from.
//
// It does not have to move. Position is not the problem, variance at that
// position is. So every stage's contract is declared here as a tool, the array
// goes out whole on every request whichever stage is being asked for, and the
// selection moves to tool_choice, which changes what the model emits while
// every cache tier survives it. What varies is a name.
//
// The invariant it rests on is that these bytes do not move between two calls
// of one run. That is narrower than "the same array forever", and the
// narrowing is now load-bearing rather than pedantic: with five strict tools
// declared, the endpoint answered
//
//	400 invalid_request_error: The compiled grammar is too large, which would
//	cause performance issues. Simplify your tool schemas or reduce the number
//	of strict tools.
//
// So the array carries the contracts this run's shape can ask for and no
// others. Every call of a staged run sends the same three, every call of a
// one-shot run the same two, and within a run nothing before the prompt
// moves. Across runs it may, and nothing reads across runs: the entry lives
// five minutes or an hour and the next run assembles its own.
//
// That last part is measured, not cited: the documented invalidation table
// says a tool_choice change costs the message blocks, which is where the
// 170,000 tokens are. On the wire it does not. An 86,639-token user block
// under a breakpoint on claude-sonnet-5 was written once and read back whole
// on the next call with tool_choice switched from review to ruling. If a
// breakpoint ever lands and reads come back zero, re-run that probe first.
const (
	StageReview   = "review"
	StageRuling   = "ruling"
	StageSynopsis = "synopsis"
	StageFindings = "findings"
	StageCohorts  = "synopsis_cohorts"
)

// stageTool is one stage's output contract, named so the model can be pointed
// at it and so a debug line and a captured file say which stage they are.
// Exported fields with tags because --debug writes this array to disk as
// part of the request it captures, and a struct of unexported fields captures
// as an empty object.
type stageTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// stageTools is every contract this run's shape may be constrained to, in a
// fixed order. The order is part of the bytes, so it is written out here
// rather than built from a map: reordering the array is the same cache miss
// as changing a schema in it.
//
// Every stage the shape can reach is declared on every one of its calls,
// including the stages this particular call is not asking for. That is the
// point: a staged run sends these three to stage one, to each cohort and to
// the ruling, and all of them are identical, so every call after the first
// reads what the first wrote.
//
// Stages the shape cannot reach are left out, because the endpoint compiles
// every strict tool into one grammar and refuses when that grammar gets too
// large. A one-shot run has no use for the partition contract and paying for
// it costs a 400.
func stageTools(opts Options) []stageTool {
	review := stageTool{
		Name: StageReview,
		Description: "Return the review of this change: the overview, a line per file, " +
			"the comments, and the verdicts. Findings only; the rulings on them " +
			"come from a later call.",
		Schema: outputSchema(),
	}
	ruling := stageTool{
		Name: StageRuling,
		Description: "Return a ruling on every finding you were given, and on no " +
			"others. This contract carries no new findings and no review text.",
		Schema: ruleSchema(),
	}
	findings := stageTool{
		Name: StageFindings,
		Description: "Return the comments and the verdicts for this change, for a run " +
			"whose overview and file lines are already written. Do not restate them; " +
			"this contract has nowhere to put them.",
		Schema: findingsSchema(),
	}
	switch {
	case opts.Pipeline == PipelineStaged:
		// The review contract is left out, and that is measured rather than
		// chosen: review + ruling + findings + cohorts is the array that got
		// the 400, and ruling + findings + cohorts is accepted. A staged run
		// that loses stage one falls back to a one-shot call, which reassembles
		// under the one-shot shape and carries the review contract then. The
		// fallback sends a different array than the call before it and reads
		// no cache, which costs nothing: the run it is rescuing has already
		// lost the call that wrote one.
		return []stageTool{ruling, findings, {
			Name: StageCohorts,
			Description: "Return what this change is - the overview and one line per file you " +
				"were shown - and a partition of those files into cohorts for the reviews " +
				"that follow. No comments and no verdicts; the judging happens in the " +
				"calls this partition feeds.",
			Schema: cohortsSchema(),
		}}
	case opts.Synopsis || opts.Pipeline == PipelineStepwise:
		// Stepwise forces the synopsis contract on turn 1 and the findings
		// contract on turn 2, and falls back to the review contract when turn 1
		// fails. This is the array the synopsis path already sends, so its size
		// is one the endpoint has accepted, and the fallback call sends the same
		// bytes as turn 1 and still reads them back.
		return []stageTool{review, ruling, findings, {
			Name: StageSynopsis,
			Description: "Return what this change is: the overview and one line per file " +
				"you were shown. No comments and no verdicts; a later call judges.",
			Schema: synopsisSchema(),
		}}
	case opts.Verify:
		return []stageTool{review, ruling}
	default:
		// No second call is coming, so the ruling contract is 443 tokens of
		// grammar nothing can be pinned to. It rides on every call of a
		// verified run because the ruling reads the prefix the review wrote;
		// with the checking pass off there is no prefix to share and no stage
		// to reach, and the review is the only contract the run can use.
		return []stageTool{review}
	}
}

// toolsTokens is what the catalogue costs, in input tokens, on every request.
//
// It has to be priced with the other fixed parts for the reason the system
// block does: a ceiling that admits a request smaller than the one that goes
// out is a ceiling that gets quoted and is wrong. This grew when the contract
// became a tool array, because a call now carries every stage's schema its
// shape can reach and not only its own, and that is the trade the caching
// buys. Estimated off the marshalled bytes, which is what the endpoint is
// actually sent.
func toolsTokens(opts Options) int {
	raw, err := json.Marshal(stageTools(opts))
	if err != nil {
		// Nothing here can fail to marshal. If it somehow does, price it high
		// rather than free: an unpriced block is the failure this exists to
		// stop, and erring high only refuses a request that was near the line.
		return 8000
	}
	return envelope.EstimateTokensLen(len(raw))
}

// anthropicTools renders the catalogue for the Messages API.
//
// strict is set on every tool. It asks the endpoint to guarantee the emitted
// input validates, which is the same promise output_config.format made and the
// reason these schemas require every property: a model that must emit a field
// cannot quietly drop the one carrying the correlation. It needs
// additionalProperties false and a required list on every object, which these
// schemas already have.
func anthropicTools(opts Options) []anthropic.ToolUnionParam {
	all := stageTools(opts)
	out := make([]anthropic.ToolUnionParam, 0, len(all))
	for _, t := range all {
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			Strict:      anthropic.Bool(true),
			InputSchema: toolInputSchema(t.Schema),
		}})
	}
	return out
}

// toolInputSchema moves a schema map into the SDK's input-schema parameter.
//
// Root keys the parameter names go in their fields and everything else goes
// through ExtraFields, which is how additionalProperties reaches the wire. It
// is written as a loop over the remaining keys rather than as a line naming
// that one key, so a root key added to a schema later is carried rather than
// silently dropped: a schema that loses additionalProperties on the way out is
// a schema strict mode rejects, and the failure would arrive as a 400 with
// nothing in the diff to explain it.
func toolInputSchema(s map[string]any) anthropic.ToolInputSchemaParam {
	p := anthropic.ToolInputSchemaParam{Properties: s["properties"]}
	if req, ok := s["required"].([]string); ok {
		p.Required = req
	}
	extra := map[string]any{}
	for k, v := range s {
		switch k {
		case "type", "properties", "required":
			// Named fields on the parameter, or the constant "object".
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		p.ExtraFields = extra
	}
	return p
}

// openAITools renders the catalogue as chat-completions functions. Same
// contracts, same order, same reason for sending all of them.
//
// strict is not set here. The field exists in that protocol, but this wire is
// aimed at whatever gateway a team puts in front of a model, and one that
// silently drops an unknown field is the good case: openai.go already records
// gateways that ignore the constraint and answer in prose, which readOpenAIResponse
// reads anyway. Asking for a guarantee this wire cannot make would be a claim
// the parse has to keep disproving.
func openAITools(opts Options) []openAITool {
	all := stageTools(opts)
	out := make([]openAITool, 0, len(all))
	for _, t := range all {
		out = append(out, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	return out
}
