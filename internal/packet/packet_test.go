package packet_test

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
)

func TestAreasClassifyFiles(t *testing.T) {
	cases := map[string]string{
		"migrations/000001_init.up.sql": "sql",
		"api/openapi.yaml":              "api",
		"web/src/Button.tsx":            "ui",
		"internal/run/run.go":           "code",
		"internal/run/run_test.go":      "tests",
	}
	for path, want := range cases {
		areas := packet.Areas(path)
		var found bool
		for _, a := range areas {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected area %q, got %v", path, want, areas)
		}
	}
}

func TestParseReviewAcceptsFencedJSON(t *testing.T) {
	in := "```json\n{\"summary\":\"does a thing\",\"findings\":[]}\n```"
	rev, err := packet.ParseReview(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if rev.Summary != "does a thing" {
		t.Fatalf("got %q", rev.Summary)
	}
}

// Every agent-authored finding must be labelled as such. The determinism
// guarantee is worthless if a reviewer cannot tell the halves apart.
func TestAgentFindingsAreLabelledLLM(t *testing.T) {
	rev := &packet.Review{Findings: []packet.Judgment{
		{File: "a.go", Rule: "r", Message: "m"},
	}}
	out := rev.ToFindings()
	if len(out) != 1 {
		t.Fatal("expected one finding")
	}
	if out[0].Source != findings.SourceLLM {
		t.Fatalf("expected source llm, got %q", out[0].Source)
	}
	if out[0].Substrate != packet.Substrate {
		t.Fatalf("expected review substrate, got %q", out[0].Substrate)
	}
}

func TestLowConfidenceIsSurfaced(t *testing.T) {
	rev := &packet.Review{Findings: []packet.Judgment{
		{File: "a.go", Rule: "r", Message: "m", Confidence: "low", Instruction: "AGENTS.md — rule"},
	}}
	f := rev.ToFindings()[0]
	if !strings.Contains(f.Context, "low confidence") {
		t.Fatalf("confidence must reach the reader: %q", f.Context)
	}
	if !strings.Contains(f.Context, "AGENTS.md") {
		t.Fatalf("the cited house rule must reach the reader: %q", f.Context)
	}
}
