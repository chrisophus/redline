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
// selection moves to tool_choice, which changes what the model emits without
// touching the tools or system caches. What varies is a name.
//
// Nothing reads a cache yet. This only makes one possible, and the invariant
// it rests on is that these bytes do not move between two calls of one run.
const (
	StageReview = "review"
	StageRuling = "ruling"
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

// stageTools is every contract a request may be constrained to, in a fixed
// order. The order is part of the bytes, so it is written out here rather than
// built from a map: reordering the array is the same cache miss as changing a
// schema in it.
//
// Every stage is declared on every call, including the stages this call is not
// asking for. That is the point. A run sends this array to the review and then
// to the ruling, and the two are identical, so the second can read what the
// first wrote.
func stageTools() []stageTool {
	return []stageTool{
		{
			Name: StageReview,
			Description: "Return the review of this change: the overview, a line per file, " +
				"the comments, and the verdicts. Call this and nothing else.",
			Schema: outputSchema(),
		},
		{
			Name: StageRuling,
			Description: "Return a ruling on every finding you were given. " +
				"Call this and nothing else.",
			Schema: ruleSchema(),
		},
	}
}

// toolsTokens is what the catalogue costs, in input tokens, on every request.
//
// It has to be priced with the other fixed parts for the reason the system
// block does: a ceiling that admits a request smaller than the one that goes
// out is a ceiling that gets quoted and is wrong. This grew when the contract
// became a tool array, because a call now carries every stage's schema and not
// only its own, and that is the trade the caching buys. Estimated off the
// marshalled bytes, which is what the endpoint is actually sent.
func toolsTokens() int {
	raw, err := json.Marshal(stageTools())
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
func anthropicTools() []anthropic.ToolUnionParam {
	all := stageTools()
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
func openAITools() []openAITool {
	all := stageTools()
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
