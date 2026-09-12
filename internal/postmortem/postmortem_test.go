package postmortem

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

// reviewed is a review that proposed three findings: one the diff settles and
// keeps, one the lookups answered and the ruling withdrew, and one the lookups
// never answered. The three are the whole point of the file, because the last
// two look identical in review.json and have opposite causes.
func reviewed() *review.Result {
	proposed := []findings.ReviewComment{
		{File: "a.go", Line: 12, Body: "the error from Close is swallowed",
			Severity: findings.SeverityWarning, Confidence: findings.ConfidenceHigh,
			Question: findings.Question{Kind: findings.QuestionDiff}},
		{File: "b.go", Line: 40, Body: "this getter is not the house style",
			Severity: findings.SeverityInfo, Confidence: findings.ConfidenceHigh,
			Question: findings.Question{Kind: findings.QuestionPrecedent,
				Subject: "getters", Ask: "does this repository write getters elsewhere?"}},
		{File: "c.go", Line: 8, Body: "nothing calls Reset any more",
			Severity: findings.SeverityWarning, Confidence: findings.ConfidenceHigh,
			Question: findings.Question{Kind: findings.QuestionCaller, Subject: "Reset"}},
	}
	cands := review.Candidates(findings.Review{Comments: proposed})

	ruled := make([]findings.ReviewComment, len(proposed))
	copy(ruled, proposed)
	ruled[0].Ruling = findings.Ruling{Verdict: findings.VerifiedKept,
		Evidence: "defer f.Close()", Grounded: true,
		Analysis: "the hunk closes the file in a defer and never reads what it returned"}
	ruled[1].Ruling = findings.Ruling{Verdict: findings.VerifiedJustified,
		Evidence: "func (u *User) Name() string", Why: "the file next door does the same",
		Grounded: true}
	ruled[1].Confidence = findings.ConfidenceLow
	ruled[2].Ruling = findings.Ruling{Verdict: findings.VerifiedUnverifiable,
		Why: "no lookup came back for this"}
	ruled[2].Confidence = findings.ConfidenceLow

	return &review.Result{
		API: "anthropic", Model: "claude-sonnet-5", Verified: true,
		CostUSD: 0.42, CostKnown: true,
		Review:     findings.Review{Comments: ruled},
		Candidates: cands,
		Questions:  []review.Question{{ID: "c2", Kind: "precedent"}, {ID: "c3", Kind: "caller"}},
		Answers: &envelope.Envelope{Expansions: []envelope.Expansion{{
			Role: envelope.Role("guideline"), File: "AGENTS.md", StartLine: 4, EndLine: 9,
			Symbol: "house style", Content: "no getters\n",
			Details: map[string]string{"answers": "c2", "foundVia": "docs"},
		}}},
	}
}

func lookup() Lookup {
	return Lookup{
		Ran: true, Model: "claude-sonnet-5", Effort: "low", Turns: 2,
		CostUSD: 0.02, CostKnown: true,
		Calls: []Call{
			{Turn: 1, Tool: "grep", Args: `{"pattern":"func (u \*User)"}`, Result: "812 bytes"},
			{Turn: 2, Tool: "record", Args: `{"file":"c_test.go"}`, Failed: true,
				Result: "error: c_test.go is a test file, and Redline holds test context back"},
		},
		Filed: []Filed{{Role: envelope.Role("guideline"), File: "AGENTS.md",
			StartLine: 4, EndLine: 9, FoundVia: "docs", Answers: "c2"}},
		Notes: []string{"nothing in the tree calls Reset outside the change itself"},
	}
}

// The ruling folds a finding it did not keep down to low confidence, so the
// review on disk cannot say what was proposed. The trace is taken before that.
func TestTheTraceKeepsTheFindingAsTheReviewerWroteIt(t *testing.T) {
	tr := Of(reviewed(), lookup())
	if len(tr.Findings) != 3 {
		t.Fatalf("findings = %d, want the three that were proposed", len(tr.Findings))
	}
	second := tr.Findings[1]
	if second.ID != "c2" || second.Confidence != findings.ConfidenceHigh {
		t.Errorf("finding = %+v, want the confidence stage one gave it", second)
	}
	if second.Ruling.Verdict != findings.VerifiedJustified {
		t.Errorf("ruling = %+v, want the one the pass wrote onto the review", second.Ruling)
	}
	if !second.Asked || tr.Findings[0].Asked {
		t.Error("the trace does not say which findings were sent to the lookups")
	}
}

// A review run with --no-verify never numbered its findings, and a postmortem
// that cannot name one cannot say anything about it.
func TestAnUncheckedReviewStillNumbersItsFindings(t *testing.T) {
	res := reviewed()
	res.Candidates, res.Questions, res.Answers = nil, nil, nil
	res.Verified = false
	tr := Of(res, Lookup{})
	if len(tr.Findings) != 3 || tr.Findings[2].ID != "c3" {
		t.Fatalf("findings = %+v, want all three numbered", tr.Findings)
	}
	if got := tr.Render(); !strings.Contains(got, "checking   did not run") {
		t.Errorf("an unchecked review must say so:\n%s", got)
	}
}

// The question this whole command exists for: a finding that came back
// unverifiable because nothing was looked up reads exactly like one that was
// checked and found wanting, and the two have opposite fixes.
func TestRenderSaysWhichFindingsTheLookupsNeverAnswered(t *testing.T) {
	got := Of(reviewed(), lookup()).Render()
	if !strings.Contains(got, "questions the lookups did not answer (1)") {
		t.Errorf("the unanswered question is not named:\n%s", got)
	}
	if !strings.Contains(got, "nothing was filed against this question") {
		t.Errorf("the finding does not say its lookup came back empty:\n%s", got)
	}
	// The one that was answered says what it was answered with.
	if !strings.Contains(got, "AGENTS.md:4-9") || !strings.Contains(got, "found by docs") {
		t.Errorf("the range the scout filed for c2 is not shown:\n%s", got)
	}
	// The ruling's working is kept nowhere else: the report shows it beside
	// the finding, and a finding the ruling did not keep is not on the report
	// the author sees.
	if !strings.Contains(got, "working: the hunk closes the file") {
		t.Errorf("the ruling's reasoning is not shown:\n%s", got)
	}
	if !strings.Contains(got, "3 findings proposed, 2 questions asked, 1 of them with something filed") {
		t.Errorf("the counts do not separate what was asked from what was answered:\n%s", got)
	}
}

// A refusal is a correction the scout was given, and on the last turn it is
// the reason a record was never filed.
func TestRenderShowsTheSearchWithItsRefusals(t *testing.T) {
	got := Of(reviewed(), lookup()).Render()
	if !strings.Contains(got, "what the scout did") || !strings.Contains(got, "turn 2") {
		t.Errorf("the search is not rendered:\n%s", got)
	}
	if !strings.Contains(got, "! record") || !strings.Contains(got, "is a test file") {
		t.Errorf("a refused call and its reason are not shown:\n%s", got)
	}
	if !strings.Contains(got, "the scout's notes") || !strings.Contains(got, "nothing in the tree calls Reset") {
		t.Errorf("the scout's own notes are not shown:\n%s", got)
	}
}

func TestWriteAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := Of(reviewed(), lookup())
	tr.Target = "the working tree"
	tr.Revision = "abc123"
	if err := Write(dir, tr); err != nil {
		t.Fatal(err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Target != "the working tree" || len(back.Findings) != 3 {
		t.Fatalf("trace = %+v", back)
	}
	if len(back.Lookup.Calls) != 2 || !back.Lookup.Calls[1].Failed {
		t.Errorf("the search did not survive the round trip: %+v", back.Lookup.Calls)
	}
	if back.Render() != tr.Render() {
		t.Error("the file renders differently from what was written")
	}
}

// Nothing to look back on is the normal state of a directory nobody has
// reviewed in, so it says what to run rather than reporting a missing file.
func TestLoadSaysWhatToRunWhenThereIsNoReview(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "redline review") {
		t.Fatalf("err = %v, want the command that would produce one", err)
	}
}

// The two questions the lookups exist to answer are not the same: whether the
// scout found anything, and whether what it found is the line the ruling then
// rested on. A run where nothing rested on a lookup paid for a stage that
// changed no decision.
func TestTheTraceSaysWhetherARulingRestedOnWhatTheScoutFetched(t *testing.T) {
	res := reviewed()
	// The precedent question's answer is the range the scout filed, and the
	// ruling on c2 quotes a line out of it.
	res.Answers.Expansions[0].Content = "// no getters in this repository, ever\nfunc (u *User) Name() string\n"
	tr := Of(res, lookup())
	if tr.Findings[1].EvidenceFrom != FromLookup {
		t.Errorf("c2 = %q, want the ruling recorded as resting on the lookup", tr.Findings[1].EvidenceFrom)
	}
	// c1's ruling quotes the diff, which is the strongest kind of finding and
	// says nothing about the scout.
	if tr.Findings[0].EvidenceFrom != FromElsewhere {
		t.Errorf("c1 = %q, want the ruling recorded as resting on something else", tr.Findings[0].EvidenceFrom)
	}
	got := tr.Render()
	if !strings.Contains(got, "from a range the lookups filed") {
		t.Errorf("the postmortem does not say the ruling rested on a lookup:\n%s", got)
	}
	if !strings.Contains(got, "1 finding ruled on a line the lookups filed") {
		t.Errorf("the count of rulings that rested on a lookup is missing:\n%s", got)
	}
}
