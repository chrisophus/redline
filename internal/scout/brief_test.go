package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
)

// The author's account of the change reaches the scout, framed as a claim to
// steer by rather than as evidence, and a run with no account has no section
// for it.
func TestTheAuthorsAccountReachesTheScoutAsAClaim(t *testing.T) {
	got := brief(Options{Diff: "d", Intent: "- Remove the nil guard\n  The guard was dead since #12; see docs/plans/nil.md\n"})
	for _, want := range []string{"What the author says the change does", "claim about the code", "docs/plans/nil.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(brief(Options{Diff: "d"}), "What the author says") {
		t.Error("a change with no account got a section about one")
	}
	long := strings.Repeat("a line of pasted output\n", 400)
	if clipped := brief(Options{Diff: "d", Intent: long}); !strings.Contains(clipped, "cut here") || len(clipped) > 4000 {
		t.Errorf("a pages-long account was not clipped and said so; %d bytes", len(clipped))
	}
}

// The prompt says how many turns the scout has and that calls in one turn
// run together, in both jobs, so it can plan rather than discover the limit
// on the closing turn.
func TestTheScoutIsToldItsTurnBudget(t *testing.T) {
	for _, opts := range []Options{
		{MaxTurns: 6},
		{MaxTurns: 6, Questions: []Question{{ID: "c1", Kind: "precedent"}}},
	} {
		got := promptFor(opts, []string{"grep"})
		if !strings.Contains(got, "You have 6 turns") {
			t.Errorf("the prompt does not say how many turns there are:\n%s", got)
		}
	}
}

// The closing brief matches the job. An exploring scout must not be told
// about findings and a ruling it has no part in.
func TestTheClosingBriefMatchesTheJob(t *testing.T) {
	exploring := closingFor(Options{})
	answering := closingFor(Options{Questions: []Question{{ID: "c1"}}})
	if exploring == answering {
		t.Fatal("the two jobs share one closing brief")
	}
	if strings.Contains(exploring, "ruled on") || strings.Contains(exploring, "question") {
		t.Errorf("the exploring scout is told about a ruling:\n%s", exploring)
	}
	if !strings.Contains(answering, "question") {
		t.Errorf("the answering scout is not told to file against its questions:\n%s", answering)
	}
}

// A rule question is answered from the files the exploring brief inlines,
// so the answering brief carries them too, and only then.
func TestARuleQuestionBringsTheRulesIntoTheBrief(t *testing.T) {
	root := docTree(t, map[string]string{"AGENTS.md": "# Style\n\nNo em dashes.\n"})
	withRule := answerBrief(Options{Root: root, Diff: "d", Questions: []Question{{ID: "c1", Kind: "rule", Ask: "is this written down?"}}})
	if !strings.Contains(withRule, "1\t# Style") {
		t.Errorf("the rules are not in the brief for a rule question:\n%s", withRule)
	}
	without := answerBrief(Options{Root: root, Diff: "d", Questions: []Question{{ID: "c1", Kind: "precedent"}}})
	if strings.Contains(without, "No em dashes") {
		t.Error("a precedent question was sent the rules file it did not ask for")
	}
}

// A record of a test file is refused with the reason, because Redline drops
// test code from the context block and the record would be paid for and never
// shown.
func TestATestFileIsRefusedWithTheReason(t *testing.T) {
	root := docTree(t, map[string]string{"internal/store/user_test.go": "package store\n\nfunc TestInsert(t *testing.T) {}\n"})
	r := newResolver(root, Limits{})
	err := r.validate(record{Role: envelope.RoleEnclosing, File: "internal/store/user_test.go", StartLine: 1, EndLine: 3})
	if err == nil || !strings.Contains(err.Error(), "test file") {
		t.Fatalf("err = %v, want a refusal naming the test file", err)
	}
}

// Nothing renders an expansion's details for the reviewer, so a block found
// by name says so in the one line the reviewer is sure to read.
func TestAContextMatchedByNameSaysSoInItsHeader(t *testing.T) {
	r := newResolver(tree(t), Limits{})
	for via, want := range map[string]bool{"grep": true, "graph": true, "read": false, "diff": false} {
		x, ok := r.resolve(record{Role: envelope.RoleCaller, File: "internal/store/user.go", StartLine: 1, EndLine: 2, Symbol: "Insert", FoundVia: via}, 1)
		if !ok {
			t.Fatalf("%s: not resolved", via)
		}
		if got := strings.Contains(x.Symbol, "matched by name"); got != want {
			t.Errorf("found via %s: symbol %q, marked by name = %v, want %v", via, x.Symbol, got, want)
		}
	}
}

// An answering scout that files a record against no question is told so,
// and the record is still kept: the evidence is real whether or not it was
// tied to a finding.
func TestAnUntaggedAnswerIsKeptAndTheScoutIsTold(t *testing.T) {
	ts := newToolset(tree(t), "", Limits{})
	ts.answering = true
	out, failed := ts.dispatch("record", json.RawMessage(`{"role":"caller","file":"internal/store/user.go","start_line":1,"end_line":2,"symbol":"Insert"}`))
	if failed || len(ts.records) != 1 {
		t.Fatalf("the record was not kept: %q", out)
	}
	if !strings.Contains(out, "tied to no question") {
		t.Errorf("the scout was not told the record answers nothing: %q", out)
	}
	// The second call the reminder asks for replaces the first rather than
	// sitting beside it.
	ts.dispatch("record", json.RawMessage(`{"role":"caller","file":"internal/store/user.go","start_line":1,"end_line":2,"symbol":"Insert","answers":"[c1]"}`))
	if len(ts.records) != 1 || ts.records[0].Answers != "c1" {
		t.Errorf("records = %+v, want the tagged record alone", ts.records)
	}
	ts.answering = false
	out, _ = ts.dispatch("record", json.RawMessage(`{"role":"caller","file":"internal/store/user.go","start_line":1,"end_line":3,"symbol":"Insert"}`))
	if strings.Contains(out, "tied to no question") {
		t.Errorf("an exploring scout was asked for a question id: %q", out)
	}
}

// The answering scout reads the same account of the change the finding was
// made from.
func TestTheAnsweringBriefCarriesTheAuthorsAccount(t *testing.T) {
	got := answerBrief(Options{Diff: "d", Intent: "- Mirror the account feed\n", Questions: []Question{{ID: "c1", Kind: "precedent"}}})
	if !strings.Contains(got, "Mirror the account feed") {
		t.Errorf("the account is missing from the answering brief:\n%s", got)
	}
}

// The system prompt and the brief carry cache breakpoints, so the bulk of
// every request after the first is read from the cache rather than paid for
// again.
func TestTheSystemPromptAndTheBriefAreCached(t *testing.T) {
	api := serve(t, msg("tool_use", toolUse("tu_1", "done", map[string]any{})))
	if _, _, err := runScout(t, api, Options{}); err != nil {
		t.Fatal(err)
	}
	req := api.requests[0]
	sys, _ := json.Marshal(req["system"])
	if !strings.Contains(string(sys), `"cache_control"`) {
		t.Errorf("the system prompt carries no cache breakpoint: %s", sys)
	}
	msgs, _ := json.Marshal(req["messages"])
	if !strings.Contains(string(msgs), `"cache_control"`) {
		t.Errorf("the brief carries no cache breakpoint: %s", msgs)
	}
}

// Once a turn has reported cached tokens, the governor prices the prefix at
// the cached rate. The same conversation under the same cap runs more turns
// when the wire says the prefix is cached than when it says nothing.
func TestTheGovernorPricesACachedPrefixAtTheCachedRate(t *testing.T) {
	turnsUnder := func(cached bool) int {
		var responses []string
		for i := 0; i < 8; i++ {
			// The wire reports the brief's size either way; what differs
			// is whether it was read from the cache.
			usage := `"usage":{"input_tokens":12000,"output_tokens":40}`
			if cached {
				usage = `"usage":{"input_tokens":1200,"output_tokens":40,"cache_read_input_tokens":10800}`
			}
			responses = append(responses, fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant",
				"model":"claude-sonnet-5","content":[%s],"stop_reason":"tool_use",%s}`,
				recordCall(fmt.Sprintf("tu_%d", i)), usage))
		}
		api := serve(t, responses...)
		// A brief large enough that pricing it at the full rate is what
		// binds, and a cap that lets the cached run go further.
		_, spend, err := Run(context.Background(), Options{
			Root: tree(t), BaseURL: api.srv.URL, APIKey: "k",
			Changed: []string{"internal/store/user.go"}, Diff: strings.Repeat("+a changed line\n", 3000),
			MaxTurns: 8, MaxTokens: 1000, MaxCostUSD: 0.09,
		})
		if err != nil {
			t.Fatal(err)
		}
		return spend.Turns
	}
	plain, cached := turnsUnder(false), turnsUnder(true)
	if cached <= plain {
		t.Errorf("a cached prefix ran %d turn(s) against %d uncached; the governor is pricing cached tokens at the full rate", cached, plain)
	}
}
