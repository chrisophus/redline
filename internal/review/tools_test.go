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

// Every call sends the same tools and the same tool_choice, whatever pass it
// is, so everything ahead of the prompt is a byte-identical prefix and a cache
// breakpoint on it can be read back by every later pass. A pass says which
// calls it wants in the prompt, not in the request's tools.
func TestEveryPassSendsTheSameToolsAndChoice(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, flatCalls(StageReview, reviewBody)),
		anthropicSSE("tool_use", 10, 5, flatCalls(StageRuling, `{"rulings":[]}`)))
	opts := Options{BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 2000}
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
	if !bytes.Equal(reviewTools, rulingTools) || !bytes.Equal(reviewChoice, rulingChoice) {
		t.Errorf("the tools or tool_choice moved between passes, so nothing after them can be read from cache:\n"+
			"review: %s %s\nruling: %s %s", reviewTools, reviewChoice, rulingTools, rulingChoice)
	}
	var c struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(reviewChoice, &c); err != nil || c.Type != "auto" {
		t.Errorf("the choice is never pinned, got %s", reviewChoice)
	}
}

// A map iterates in a different order every time, and the schemas travel as
// maps, one of them through ExtraFields. If any of that reached the wire in an
// unstable order the prefix would differ between two calls of one run and the
// cache would miss for a reason no diff could show.
func TestTheToolsBlockSerializesTheSameEveryTime(t *testing.T) {
	first, err := json.Marshal(anthropicTools(true, nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		again, err := json.Marshal(anthropicTools(true, nil))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("the tools block serialized differently on pass %d:\n%s\n%s", i, first, again)
		}
	}
}

// No tool is strict: strict tools compile into one grammar with a size limit,
// and the calls are checked here instead. Every object still closes
// additionalProperties, so the model is told extra fields are not wanted, and
// the check refuses them.
func TestNoToolIsStrictAndEveryObjectIsClosed(t *testing.T) {
	raw, err := json.Marshal(anthropicTools(true, nil))
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
	if len(tools) != len(callTools(true, nil)) {
		t.Fatalf("%d tool(s) reached the wire, %d were declared", len(tools), len(callTools(true, nil)))
	}
	for _, tool := range tools {
		if tool.Strict != nil && *tool.Strict {
			t.Errorf("%s: strict is set, which brings back the grammar limit", tool.Name)
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

// A review that arrives as calls, the way the wire really sends them, has to
// come out the other end intact.
func TestTheReviewIsReadFromTheCalls(t *testing.T) {
	api := serveSSE(t, anthropicSSE("tool_use", 10, 5,
		flatCalls(StageReview, reviewBody)))
	res := &Result{System: "s", Prompt: "p", Stage: StageReview}
	c, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 2000,
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

// A model that answers in prose is asked once for the calls. If it answers in
// prose again, the parse reads the prose, and says what is wrong with it when
// it cannot. Reporting it as an empty response would read as a review that
// found nothing.
func TestProseInsteadOfTheToolCallIsStillRead(t *testing.T) {
	api := serveSSE(t, anthropicSSE("end_turn", 10, 5, anthropicText(0, reviewBody)))
	res := &Result{System: "s", Prompt: "p", Stage: StageReview}
	c, err := completeAnthropic(context.Background(), Options{
		BaseURL: api.srv.URL, APIKey: "k", Model: "claude-sonnet-5", MaxTokens: 2000,
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

// The check in calls.go understands a handful of keywords, so a schema that
// used another would be advice the check never enforces. And the house rule
// from schema.go holds: every field is required, so a model cannot quietly
// drop the one carrying the correlation.
func TestEveryToolUsesOnlyKeywordsTheCheckEnforces(t *testing.T) {
	// Anything outside this set is either a keyword validate ignores or a
	// typo, and both want a look.
	allowed := map[string]bool{
		"type": true, "description": true, "enum": true, "items": true,
		"properties": true, "required": true, "additionalProperties": true,
	}
	var walk func(path string, node map[string]any)
	walk = func(path string, node map[string]any) {
		for k := range node {
			if !allowed[k] {
				t.Errorf("%s: %q is not a keyword the check enforces", path, k)
			}
		}
		if node["type"] != "object" {
			if items, ok := node["items"].(map[string]any); ok {
				walk(path+"[]", items)
			}
			return
		}
		if node["additionalProperties"] != false {
			t.Errorf("%s: an object must close additionalProperties", path)
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
	for _, tool := range callTools(true, nil) {
		walk(tool.Name, tool.Schema)
	}
}
