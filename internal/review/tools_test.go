package review

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// toolsBlockOf pulls the raw tools and tool_choice bytes out of a request as
// the server received them. Raw rather than decoded, because what matters here
// is the bytes: a cache is a prefix match, and two requests whose tools decode
// to the same value but serialize differently miss.
func toolsBlockOf(t *testing.T, raw []byte) (tools, choice []byte) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the request is not JSON: %v", err)
	}
	if len(body["tools"]) == 0 {
		t.Fatalf("no tools were sent; the contract has to ride as one, got keys %v", keysOf(body))
	}
	return body["tools"], body["tool_choice"]
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The whole point of the change this file tests. The review and the ruling
// send the same tools, and only the name in tool_choice differs, so everything
// ahead of the prompt is a byte-identical prefix and a cache breakpoint on it
// can be read back. A schema that varied per stage sat in front of the system
// block and cost the shared prefix twice, measured at 171,690 and 170,601
// tokens written with nothing read.
func TestEveryStageSendsTheSameToolsAndDiffersOnlyInTheChoice(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, "{}")))
	opts := Options{BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100}
	for _, stage := range []string{StageReview, StageRuling} {
		res := &Result{System: "the system prompt", Prompt: "the whole prompt", Stage: stage}
		if _, err := completeAnthropic(context.Background(), opts, res); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	seen := api.seen()
	if len(seen) != 2 {
		t.Fatalf("expected two requests, got %d", len(seen))
	}
	reviewTools, reviewChoice := toolsBlockOf(t, seen[0])
	rulingTools, rulingChoice := toolsBlockOf(t, seen[1])

	if !bytes.Equal(reviewTools, rulingTools) {
		t.Errorf("the tools block moved between stages, so nothing after it can be read from cache:\n"+
			"review: %s\nruling: %s", reviewTools, rulingTools)
	}
	if bytes.Equal(reviewChoice, rulingChoice) {
		t.Errorf("both stages asked for the same tool; the stage has to be the thing that varies, got %s", reviewChoice)
	}
	for stage, choice := range map[string][]byte{StageReview: reviewChoice, StageRuling: rulingChoice} {
		var c struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(choice, &c); err != nil {
			t.Fatalf("%s: tool_choice is not readable: %v", stage, err)
		}
		if c.Type != "tool" || c.Name != stage {
			t.Errorf("%s: the model must be forced to that stage's tool, got type=%q name=%q", stage, c.Type, c.Name)
		}
	}
}

// A map iterates in a different order every time, and the schemas travel as
// maps, one of them through ExtraFields. If any of that reached the wire in an
// unstable order the prefix would differ between two calls of one run and the
// cache would miss for a reason no diff could show.
func TestTheToolsBlockSerializesTheSameEveryTime(t *testing.T) {
	first, err := json.Marshal(anthropicTools())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		again, err := json.Marshal(anthropicTools())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("the tools block serialized differently on pass %d:\n%s\n%s", i, first, again)
		}
	}
}

// strict is what replaces the guarantee output_config.format used to make: the
// emitted input validates against the schema. It needs additionalProperties
// false on every object, so both have to survive the trip onto the wire.
func TestEveryContractGoesOutStrictAndClosed(t *testing.T) {
	raw, err := json.Marshal(anthropicTools())
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name        string         `json:"name"`
		Strict      *bool          `json:"strict"`
		InputSchema map[string]any `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != len(stageTools()) {
		t.Fatalf("%d tool(s) reached the wire, %d were declared", len(tools), len(stageTools()))
	}
	for _, tool := range tools {
		if tool.Strict == nil || !*tool.Strict {
			t.Errorf("%s: strict must be set, or the schema is advice", tool.Name)
		}
		if tool.InputSchema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties must reach the wire as false, got %v",
				tool.Name, tool.InputSchema["additionalProperties"])
		}
		if _, ok := tool.InputSchema["properties"]; !ok {
			t.Errorf("%s: the schema lost its properties on the way out", tool.Name)
		}
	}
}

// The body the parser reads is now the forced call's input rather than the
// text blocks, so a review that arrives the way the wire really sends it has
// to come out the other end intact.
func TestTheReviewIsReadFromTheForcedToolCall(t *testing.T) {
	api := serveSSE(t, anthropicSSE("tool_use", 10, 5,
		anthropicToolUse(0, "toolu_1", StageReview, reviewBody)))
	res := &Result{System: "s", Prompt: "p", Stage: StageReview}
	c, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, res)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.absorb(Options{}, res.stage(), c); err != nil {
		t.Fatal(err)
	}
	if len(res.Review.Comments) != 1 || res.Review.Comments[0].Body != "x is unused" {
		t.Fatalf("the review must be read off the tool call: %+v", res.Review)
	}
}

// A model that ignores a forced tool_choice and answers in prose is a broken
// contract, and the parse says so with the body in front of it. Reporting it
// as an empty response instead would read as a review that found nothing.
func TestProseInsteadOfTheToolCallIsStillRead(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, reviewBody)))
	res := &Result{System: "s", Prompt: "p", Stage: StageReview}
	c, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 100,
	}, res)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.absorb(Options{}, res.stage(), c); err != nil {
		t.Fatalf("a body that arrived as text must still be read: %v", err)
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("the review must survive arriving as prose: %+v", res.Review)
	}
}

// The batch tier is the eval's cheap arm, and the whole value of it is that it
// measures what ships. Its params are copied field by field into another type,
// so the copy is where a contract goes missing: a batched request with no
// tools carries no contract and comes back as prose.
func TestTheBatchedRequestCarriesTheSameContractAsTheStreamedOne(t *testing.T) {
	res := &Result{System: "s", Prompt: "p", Stage: StageRuling}
	streamed := anthropicParams(Options{Model: "claude-sonnet-5", MaxTokens: 100}, res)
	batched := batchParams(streamed)

	for _, f := range []struct {
		name      string
		want, got any
	}{
		{"tools", streamed.Tools, batched.Tools},
		{"tool_choice", streamed.ToolChoice, batched.ToolChoice},
		{"system", streamed.System, batched.System},
		{"messages", streamed.Messages, batched.Messages},
		{"output_config", streamed.OutputConfig, batched.OutputConfig},
	} {
		want, err := json.Marshal(f.want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(f.got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s did not survive the copy into the batch request:\nstreamed: %s\nbatched:  %s",
				f.name, want, got)
		}
	}
	if len(batched.Tools) == 0 {
		t.Error("the batched request went out with no output contract at all")
	}
}

// strict mode is enforced by the endpoint, not here, so a schema it rejects is
// a 400 at the end of a real run and nothing a dry run would catch. These are
// the rules it applies, walked over every object in every contract, because
// two more contracts are due to join this array and the failure they would
// cause is expensive to diagnose from the error alone.
//
// The required check is the house rule from schema.go, which until now was
// only asserted at the root of the review's schema: a model that must emit a
// field cannot quietly drop the one carrying the correlation.
func TestEveryContractObeysTheRulesStrictModeEnforces(t *testing.T) {
	// Keywords that describe a shape. Anything outside this set is either a
	// validation keyword strict mode does not accept (pattern, format,
	// minLength and the rest) or a typo, and both want a look.
	allowed := map[string]bool{
		"type": true, "description": true, "enum": true, "items": true,
		"properties": true, "required": true, "additionalProperties": true,
	}
	var walk func(path string, node map[string]any)
	walk = func(path string, node map[string]any) {
		for k := range node {
			if !allowed[k] {
				t.Errorf("%s: %q is not a keyword strict mode takes", path, k)
			}
		}
		if node["type"] != "object" {
			if items, ok := node["items"].(map[string]any); ok {
				walk(path+"[]", items)
			}
			return
		}
		if node["additionalProperties"] != false {
			t.Errorf("%s: an object must close additionalProperties under strict mode", path)
		}
		props, _ := node["properties"].(map[string]any)
		req, _ := node["required"].([]string)
		named := map[string]bool{}
		for _, r := range req {
			named[r] = true
			if _, ok := props[r]; !ok {
				t.Errorf("%s: required names %q, which the object does not define", path, r)
			}
		}
		for name, child := range props {
			if !named[name] {
				t.Errorf("%s: %q is defined but not required, so the model may drop it", path, name)
			}
			if c, ok := child.(map[string]any); ok {
				walk(path+"."+name, c)
			}
		}
	}
	for _, tool := range stageTools() {
		walk(tool.Name, tool.Schema)
	}
}
