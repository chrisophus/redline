package review

import "testing"

// The brief arm is the one path whose reply nothing else constrains: it sends
// no tools, so the tool grammar that holds every other stage to its contract
// is not there to hold this one. Prose asked for the object instead, and the
// model answered in a different wrapper on every sample.
//
// output_config.format is why the other stages cannot have this. It renders
// ahead of the system block, so a run whose calls ask for different contracts
// moves bytes in front of the prefix they share and each later call reads
// nothing back. A brief run asks for the review contract and no other, so it
// has no variance to pay for.
func TestTheBriefReviewCallDeclaresItsContract(t *testing.T) {
	opts := Options{Model: "claude-sonnet-5", MaxTokens: 100, Brief: true}
	params := anthropicParams(opts, &Result{Stage: StageReview})

	if len(params.Tools) != 0 {
		t.Fatalf("the brief arm sent %d tool(s); the format is only safe because it sends none",
			len(params.Tools))
	}
	if params.OutputConfig.Format.Schema == nil {
		t.Fatal("the brief review call carries no output format, so its reply is unconstrained")
	}
	req, ok := params.OutputConfig.Format.Schema["required"].([]string)
	if !ok || len(req) != 4 {
		t.Errorf("format schema required = %v, want the four review fields", params.OutputConfig.Format.Schema["required"])
	}
}

// Every other stage keeps the tool and must not carry a format, because two
// calls in one run that declare different ones is the case that was measured
// reading 171,690 and 170,601 tokens back as nothing.
func TestOtherStagesCarryNoOutputFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		opts  Options
		stage string
	}{
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

// BriefKeepsTools is the arm that separates the two halves of Brief. The whole
// value of the comparison rests on it differing from the brief arm in exactly
// one way: the grammar comes back and the output format goes away, so what is
// varied is the emission and nothing else. If the prompt, the thinking setting
// or anything else moves with it, the run measures two changes and settles
// neither.
func TestTheBriefWithToolsArmDiffersOnlyInItsEmission(t *testing.T) {
	base := Options{Model: "claude-sonnet-5", MaxTokens: 100, Brief: true}
	withTools := base
	withTools.BriefKeepsTools = true

	free := anthropicParams(base, &Result{Stage: StageReview})
	grammar := anthropicParams(withTools, &Result{Stage: StageReview})

	if len(grammar.Tools) == 0 {
		t.Error("the arm that keeps its tools sent none, so it is not the arm it claims to be")
	}
	if grammar.OutputConfig.Format.Schema != nil {
		t.Error("this arm sent the grammar and an output format together, which is two contracts and two differences")
	}
	if len(free.Tools) != 0 || free.OutputConfig.Format.Schema == nil {
		t.Error("the brief arm is no longer free-form, so the comparison has lost its other side")
	}
	// Thinking is disabled on both, or the emission is not the only thing that
	// moved between them.
	if grammar.Thinking.OfDisabled == nil || free.Thinking.OfDisabled == nil {
		t.Error("thinking is not disabled on both arms")
	}
	// Same prompt on both, which systemFor gives them by keying on Brief alone.
	if systemFor(base, StageReview) != systemFor(withTools, StageReview) {
		t.Error("the two arms are running different prompts, so the comparison is not about the emission")
	}
}

// The reservation asks about the wire and the arm together, because either one
// can put the catalogue back on the request.
func TestBriefWithToolsReservesItsTools(t *testing.T) {
	o := Options{Brief: true, BriefKeepsTools: true, API: APIAnthropic}
	if o.briefSendsNoTools() {
		t.Error("this arm keeps its tools, so the budget has to reserve for them")
	}
}
