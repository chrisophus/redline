package review

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// No stage carries an output format, because it renders ahead of the system
// block: a run whose calls declare different ones moves bytes in front of the
// prefix they share, which was measured reading 171,690 and 170,601 tokens
// back as nothing.
func TestNoStageCarriesAnOutputFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  Options
		stage string
	}{
		{"review", Options{Model: "claude-sonnet-5", MaxTokens: 100}, StageReview},
		{"ruling", Options{Model: "claude-sonnet-5", MaxTokens: 100}, StageRuling},
		{"describing", Options{Model: "claude-sonnet-5", MaxTokens: 100}, StageSynopsis},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := anthropicParams(tc.opts, &Result{Stage: tc.stage})
			if params.OutputConfig.Format.Schema != nil {
				t.Error("this stage declared an output format, which moves bytes ahead of the shared prefix")
			}
			if len(params.Tools) == 0 {
				t.Error("this stage sent no tools, so nothing holds it to its contract")
			}
		})
	}
}

// claude-fable-5-1 used to answer 400 to a pinned tool_choice, with
// tool_choice: type "tool" and "any" are not supported for this model. The
// choice is never pinned now, on any model, since forcing it is incompatible
// with thinking on this API: every model gets the same auto choice and the
// same catalogue.
func TestEveryModelGetsTheSameUnpinnedToolChoice(t *testing.T) {
	text := func(p anthropic.MessageNewParams) string {
		var b strings.Builder
		for _, blk := range p.Messages[0].Content {
			if blk.OfText != nil {
				b.WriteString(blk.OfText.Text)
			}
		}
		return b.String()
	}

	sonnet := anthropicParams(Options{Model: "claude-sonnet-5", MaxTokens: 100}, &Result{Stage: StageReview})
	if sonnet.ToolChoice.OfAuto == nil {
		t.Error("the choice must never be pinned, even on a model that accepts the pin")
	}

	fable := anthropicParams(Options{Model: "claude-fable-5-1", MaxTokens: 100}, &Result{Stage: StageReview})
	if fable.ToolChoice.OfTool != nil || fable.ToolChoice.OfAny != nil {
		t.Error("pinned the tool choice on a model that answers 400 to it")
	}
	if fable.ToolChoice.OfAuto == nil {
		t.Error("a model that refuses the pin was sent no tool choice at all")
	}
	if !strings.Contains(text(fable), CallComment) {
		t.Error("nothing in the request says which calls answer this pass")
	}
	if len(fable.Tools) != len(sonnet.Tools) {
		t.Error("every model gets the same catalogue")
	}
}
