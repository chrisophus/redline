package review

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/chrisophus/redline/internal/envelope"
)

// toolsTokens is what the catalogue costs, in input tokens, on every request.
//
// It has to be priced with the other fixed parts for the reason the system
// block does: a ceiling that admits a request smaller than the one that goes
// out is a ceiling that gets quoted and is wrong. This grew when the contract
// became a tool array, because a call now carries every stage's schema its
// shape can reach and not only its own, and that is the trade the caching
// buys. Estimated off the marshalled bytes, which is what the endpoint is
// actually sent.
func toolsTokens(pulls bool, looks []string) int {
	raw, err := json.Marshal(callTools(pulls, looks))
	if err != nil {
		// Nothing here can fail to marshal. If it somehow does, price it high
		// rather than free: an unpriced block is the failure this exists to
		// stop, and erring high only refuses a request that was near the line.
		return 8000
	}
	return envelope.EstimateTokensLen(len(raw))
}

// anthropicTools renders the tools for the Messages API. None is strict: see
// calls.go for why the calls are checked here instead.
func anthropicTools(pulls bool, looks []string) []anthropic.ToolUnionParam {
	all := callTools(pulls, looks)
	out := make([]anthropic.ToolUnionParam, 0, len(all))
	for _, t := range all {
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
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
// silently dropped: a schema that loses additionalProperties on the way out
// tells the model extra fields are fine when the check refuses them.
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

// openAITools renders the tools as chat-completions functions, in the same
// order and with the same schemas.
func openAITools(pulls bool, looks []string) []openAITool {
	all := callTools(pulls, looks)
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
