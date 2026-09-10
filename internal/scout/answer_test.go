package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
)

// Answering is the better half of the scout's job, and the brief is what makes
// it that. Every question arrives with the claim it belongs to, so the search
// is for the thing somebody actually doubted rather than for whatever the
// diff's shape suggested.
func TestTheBriefCarriesEachClaimAndItsQuestion(t *testing.T) {
	got := answerBrief(Options{
		Diff: "--- feed.go\n+cols := awsOfferFeedRawColumns\n",
		Questions: []Question{{
			ID: "c3", Kind: "precedent",
			Claim:   "The hard-coded column list will silently mis-stage after a migration.",
			Ask:     "does anything else in this repository hard-code a CopyFrom column list?",
			Subject: "awsOfferFeedRawColumns",
			File:    "internal/feed/offer.go", Line: 42,
		}},
	})
	for _, want := range []string{
		"[c3] precedent",
		"internal/feed/offer.go:42",
		"the finding claims: The hard-coded column list",
		"look up: awsOfferFeedRawColumns",
		"awsOfferFeedRawColumns",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief is missing %q:\n%s", want, got)
		}
	}
}

// The two jobs differ in three places and nowhere else. A scout given
// questions must not be told to go exploring, and one given none must not be
// told to answer questions it does not have.
func TestTheModeIsChosenByWhetherThereAreQuestions(t *testing.T) {
	exploring := Options{Diff: "d"}
	answering := Options{Diff: "d", Questions: []Question{{ID: "c1", Kind: "precedent"}}}

	if !strings.Contains(promptFor(exploring, nil), "You gather context") {
		t.Error("with no questions the scout should be exploring")
	}
	if !strings.Contains(promptFor(answering, nil), "checking a code review's findings") {
		t.Error("with questions the scout should be checking claims")
	}
	if fragmentFor(exploring) == fragmentFor(answering) {
		t.Error("the reviewer has to be told which of the two produced its context")
	}
	if !strings.Contains(fragmentFor(answering), "not the same as one that came back negative") {
		t.Error("the ruling must not read an unreached question as a negative answer")
	}
}

// The scout finds the evidence and does not rule on it. A scout that starts
// forming opinions stops fetching, which is the failure the exploring prompt
// already guards against and the answering one has more reason to.
func TestTheAnsweringPromptForbidsRuling(t *testing.T) {
	got := answerPrompt([]string{"grep", "read_lines", "record", "done"})
	if !strings.Contains(got, "You do not\nrule") {
		t.Error("the scout was not told that ruling is somebody else's turn")
	}
	if !strings.Contains(got, "Evidence against the finding is worth more") {
		t.Error("the point of the pass is finding what refutes a claim; say so")
	}
	if !strings.Contains(got, "record where it is") && !strings.Contains(got, "Record where") {
		t.Error("the rule that holds the design up is missing: record locations, never content")
	}
}

// One range can genuinely answer two findings, and deduplicating those into
// one would leave the second looking unchecked.
func TestOneRangeCanAnswerTwoQuestions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newResolver(root, Limits{})
	got := r.Expansions([]record{
		{Role: envelope.RoleSibling, File: "a.go", StartLine: 1, EndLine: 2, Symbol: "F", Answers: "c1"},
		{Role: envelope.RoleSibling, File: "a.go", StartLine: 1, EndLine: 2, Symbol: "F", Answers: "c2"},
		{Role: envelope.RoleSibling, File: "a.go", StartLine: 1, EndLine: 2, Symbol: "F", Answers: "c2"},
	})
	if len(got) != 2 {
		t.Fatalf("two questions answered by one range are two answers, got %d", len(got))
	}
	if got[0].Details["answers"] == "" || got[1].Details["answers"] == "" {
		t.Fatalf("an answer with no question attached tells the ruling nothing: %+v", got)
	}
	if got[0].Details["answers"] == got[1].Details["answers"] {
		t.Fatalf("both answers point at the same finding: %+v", got)
	}
}
