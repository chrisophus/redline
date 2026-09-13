package review

import "testing"

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
	// Thinking stays off. The ladder ran this prompt with thinking disabled and
	// caught 8 of 31 expectations against the product's 7, so the reasoning
	// tokens were not what found the defects, and a brief run that turned them
	// back on would pay for them twice.
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
