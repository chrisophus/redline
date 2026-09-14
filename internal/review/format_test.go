package review

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// The brief review call sends the catalogue like every other stage.
//
// It did not always. Brief used to drop the tools and ask for the object in
// prose, with an output format declared to bound the reply. That shape only
// existed on this wire: completeOpenAI sends the catalogue on every call and
// has no way to be told otherwise, so the two wires ran different reviews. The
// emissions were measured apart over eleven fixtures at three samples, 17/38
// free-form against 15/38 over the grammar with a noise floor near 3.6, and
// staged-empty-partition, the single fixture that separated them, scored 1/3
// against 0/3 at five samples. Nothing distinguished them, so the grammar wins
// on the one ground that is not a measurement: both wires can send it.
func TestTheBriefReviewCallSendsTheSameGrammarAsEveryOtherStage(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", MaxTokens: 100, Brief: true}
	params := anthropicParams(opts, &Result{Stage: StageReview})

	if len(params.Tools) == 0 {
		t.Error("the brief review sent no tools, so this wire is reviewing under a contract the OpenAI wire cannot match")
	}
	if params.OutputConfig.Format.Schema != nil {
		t.Error("the brief review declared an output format; the grammar carries the contract and a gateway ignores the format")
	}
	// Thinking stays off. Turning it back on was measured on 2026-09-14 and left
	// the short prompt's stub replies at 5 of 42 against 17 of 126 with it off,
	// so the setting was left alone.
	if params.Thinking.OfDisabled == nil {
		t.Error("thinking is not disabled on the brief review")
	}
}

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
		{"review on a brief run", Options{Model: "claude-sonnet-5", MaxTokens: 100, Brief: true}, StageReview},
		{"ruling on a brief run", Options{Model: "claude-sonnet-5", MaxTokens: 100, Brief: true}, StageRuling},
		{"review without brief", Options{Model: "claude-sonnet-5", MaxTokens: 100}, StageReview},
		{"ruling without brief", Options{Model: "claude-sonnet-5", MaxTokens: 100}, StageRuling},
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

// Brief changes the prompt and the thinking setting. It must not change the
// request's shape, or the two wires come apart again in a way no test on one
// of them alone would catch.
func TestBriefChangesThePromptAndNotTheGrammar(t *testing.T) {
	base := Options{Model: "claude-sonnet-5", MaxTokens: 100}
	brief := base
	brief.Brief = true

	long := anthropicParams(base, &Result{Stage: StageReview})
	short := anthropicParams(brief, &Result{Stage: StageReview})

	if len(long.Tools) != len(short.Tools) {
		t.Errorf("brief sent %d tool(s) and the long prompt sent %d; the grammar is meant to be the same",
			len(short.Tools), len(long.Tools))
	}
	if systemFor(base, StageReview) == systemFor(brief, StageReview) {
		t.Error("brief is running the shipped prompt, so it is not the arm it claims to be")
	}
}

// Turning thinking off and asking for effort are set from different places and
// neither knew about the other. The endpoint refuses the combination:
// output_config.effort 'xhigh' with thinking disabled answers 400 on
// claude-opus-5, saying to use 'high' or below or to enable thinking. Both are
// documented flag values, so --brief --effort xhigh was a run that could not
// start. The same request at 'high' returns 200.
func TestTheEffortComesDownWhenThinkingIsOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		brief  bool
		effort string
		want   string
	}{
		{"xhigh on a brief run comes down", true, "xhigh", "high"},
		{"max on a brief run comes down", true, "max", "high"},
		{"high on a brief run is already accepted", true, "high", "high"},
		{"low on a brief run is untouched", true, "low", "low"},
		{"xhigh stands when thinking is on", false, "xhigh", "xhigh"},
		{"max stands when thinking is on", false, "max", "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Model: "claude-opus-5", MaxTokens: 100, Brief: tc.brief, Effort: tc.effort}
			params := anthropicParams(opts, &Result{Stage: StageReview})
			if got := string(params.OutputConfig.Effort); got != tc.want {
				t.Errorf("sent effort %q, want %q", got, tc.want)
			}
		})
	}
}

// claude-fable-5-1 answers 400 to a pinned choice, with tool_choice: type
// "tool" and "any" are not supported for this model. Auto is accepted and it
// does call the tool, so a model that refuses the pin is asked for the stage in
// the prompt rather than failing the run. The pinned request must not pick up
// that instruction, because it is the one every other run sends.
func TestAModelThatRefusesAForcedToolChoiceIsAskedInstead(t *testing.T) {
	text := func(p anthropic.MessageNewParams) string {
		var b strings.Builder
		for _, blk := range p.Messages[0].Content {
			if blk.OfText != nil {
				b.WriteString(blk.OfText.Text)
			}
		}
		return b.String()
	}

	pinned := anthropicParams(Options{Model: "claude-sonnet-5", MaxTokens: 100}, &Result{Stage: StageReview})
	if pinned.ToolChoice.OfTool == nil {
		t.Error("a model that accepts the pin was not pinned to its stage")
	}
	if strings.Contains(text(pinned), "do not answer in prose") {
		t.Error("the pinned request carried the fallback instruction, which every run would then pay for")
	}

	asked := anthropicParams(Options{Model: "claude-fable-5-1", MaxTokens: 100}, &Result{Stage: StageReview})
	if asked.ToolChoice.OfTool != nil {
		t.Error("pinned the tool choice on a model that answers 400 to it")
	}
	if asked.ToolChoice.OfAuto == nil {
		t.Error("a model that refuses the pin was sent no tool choice at all")
	}
	if !strings.Contains(text(asked), StageReview) {
		t.Error("nothing in the request says which stage it is, and tool_choice no longer says it")
	}
	if len(asked.Tools) != len(pinned.Tools) {
		t.Error("the fallback changed the catalogue; only the choice and the instruction change")
	}
}
