package review

import "testing"

// The model a response names reaches the result on both wires. The eval refuses
// a sample served by a different model than it asked for, and that check can
// only be as good as the field it reads: a result that carried the requested
// model instead would pass every substitution.
func TestTheServedModelIsTheOneTheResponseNamed(t *testing.T) {
	c, _, err := readOpenAIResponse([]byte(`{"model":"gpt-5-2025-08-07",
		"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.model != "gpt-5-2025-08-07" {
		t.Errorf("openai completion model = %q, want the response's", c.model)
	}

	res := &Result{Stage: StageReview, Model: "claude-sonnet-5", FilesShown: 1}
	err = res.absorb(Options{}.withDefaults(), StageReview, completion{
		text: `{"overview":"a rename and nothing else",
			"files":[{"path":"a.go","summary":"renames the receiver"}],
			"comments":[],"verdicts":[]}`,
		stopReason: "end_turn",
		model:      "claude-sonnet-5-20260901",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ServedModel != "claude-sonnet-5-20260901" {
		t.Errorf("ServedModel = %q, want the completion's", res.ServedModel)
	}
	if res.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q; absorb must leave the requested model alone", res.Model)
	}
}
