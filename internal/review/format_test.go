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
