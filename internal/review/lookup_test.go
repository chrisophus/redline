package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/findings"

	"github.com/chrisophus/redline/internal/change"
)

// fakeLooker answers with what it was asked, so a test can tell a call that
// reached the tree from one that was rejected before it got there.
type fakeLooker struct {
	greps   []string
	reads   []string
	symbols []string
	history []string
	docs    int
	answer  string
	err     error
	// calls is what this Looker reports it can serve. Empty means every
	// lookup, so a test that does not care about the catalogue gets one.
	calls []string
}

func (f *fakeLooker) Calls() []string {
	if len(f.calls) == 0 {
		return LookCalls
	}
	return f.calls
}

func (f *fakeLooker) ListDocs(filter string) (string, error) {
	f.docs++
	return f.answer, f.err
}

func (f *fakeLooker) SymbolContext(symbol string) (string, error) {
	f.symbols = append(f.symbols, symbol)
	return f.answer, f.err
}

func (f *fakeLooker) LineHistory(path string, start, end int) (string, error) {
	f.history = append(f.history, path)
	return f.answer, f.err
}

func (f *fakeLooker) Grep(pattern, glob string) (string, error) {
	f.greps = append(f.greps, pattern+"|"+glob)
	return f.answer, f.err
}

func (f *fakeLooker) ReadLines(path string, start, end int) (string, error) {
	f.reads = append(f.reads, path)
	return f.answer, f.err
}

// The lookups are off unless the run was given somewhere to look. A catalogue
// that offered them anyway would be a pass told it can check a claim and then
// answered "no lookup is available".
func TestTheLookupToolsAreOffWithoutALooker(t *testing.T) {
	names := func(looks []string) string {
		var out []string
		for _, tl := range callTools(true, false, false, looks) {
			out = append(out, tl.Name)
		}
		return strings.Join(out, ",")
	}
	off := names(nil)
	for _, name := range LookCalls {
		if strings.Contains(off, name) {
			t.Errorf("tools = %s, want no %s", off, name)
		}
	}
	on := names(LookCalls)
	for _, name := range LookCalls {
		if !strings.Contains(on, name) {
			t.Errorf("tools = %s, want %s", on, name)
		}
	}
}

// A Looker that cannot serve a lookup keeps it off the catalogue. gorefactor
// is not installed everywhere, and a tool on the wire that answers "not
// installed" costs the pass a turn and teaches it nothing about the code.
func TestACatalogueOffersOnlyWhatTheLookerCanServe(t *testing.T) {
	look := &fakeLooker{calls: []string{CallGrep, CallRead, CallDocs, CallHistory}}
	var names []string
	for _, tl := range callTools(true, false, false, lookCallsFor(look)) {
		names = append(names, tl.Name)
	}
	got := strings.Join(names, ",")
	if strings.Contains(got, CallSymbol) {
		t.Errorf("tools = %s, want no %s from a Looker that cannot serve it", got, CallSymbol)
	}
	for _, want := range []string{CallGrep, CallRead, CallDocs, CallHistory} {
		if !strings.Contains(got, want) {
			t.Errorf("tools = %s, want %s", got, want)
		}
	}
}

// A name the Looker reports that this package does not define is dropped
// rather than put on the wire. A tool nothing dispatches would come back "no
// tool named", which reads to the pass as its own mistake.
func TestAnUnknownLookCallNeverReachesTheCatalogue(t *testing.T) {
	look := &fakeLooker{calls: []string{CallGrep, "rm_rf"}}
	if got := lookCallsFor(look); strings.Join(got, ",") != CallGrep {
		t.Errorf("look calls = %v, want only %s", got, CallGrep)
	}
}

// Each new lookup reaches its own method with the arguments as asked, and is
// counted for the trace the way grep and read_lines are.
func TestTheNewLookupsReachTheTree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		call  string
		input map[string]any
		check func(*testing.T, *fakeLooker)
	}{
		{"docs", CallDocs, map[string]any{}, func(t *testing.T, f *fakeLooker) {
			if f.docs != 1 {
				t.Errorf("list_docs reached the tree %d time(s), want 1", f.docs)
			}
		}},
		{"symbol", CallSymbol, map[string]any{"symbol": "Store.Insert"}, func(t *testing.T, f *fakeLooker) {
			if len(f.symbols) != 1 || f.symbols[0] != "Store.Insert" {
				t.Errorf("symbols = %v, want the symbol as asked", f.symbols)
			}
		}},
		{"history", CallHistory, map[string]any{"path": "store.go", "start_line": 3, "end_line": 9},
			func(t *testing.T, f *fakeLooker) {
				if len(f.history) != 1 || f.history[0] != "store.go" {
					t.Errorf("history = %v, want the path as asked", f.history)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			look := &fakeLooker{answer: "what the tree said"}
			c := newCollector(StageFindings, passExpect{}, nil, look, false)
			input, err := json.Marshal(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			results, done, rejected := c.take([]toolCall{{ID: "t1", Name: tc.call, Input: input}})
			if rejected != 0 || done {
				t.Fatalf("rejected=%d done=%v; a lookup is neither refused nor an ending", rejected, done)
			}
			if len(results) != 1 || results[0].isError {
				t.Fatalf("results = %+v, want the answer back", results)
			}
			if !strings.Contains(results[0].content, "what the tree said") {
				t.Errorf("content = %q, want what the tree said", results[0].content)
			}
			if c.looked != 1 {
				t.Errorf("looked = %d, want the lookup counted for the trace", c.looked)
			}
			tc.check(t, look)
		})
	}
}

// The catalogue is fixed for a run and the permission is per pass. Checking a
// claim is what a judging pass does; a describing pass has nothing to check,
// and the ruling was assembled around the answers of a lookup pass of its own.
func TestOnlyTheJudgingPassesMayLookThingsUp(t *testing.T) {
	for _, tc := range []struct {
		stage string
		want  bool
	}{
		{StageFindings, true},
		{StageReview, true},
		{StageSynopsis, false},
		{StageRuling, false},
	} {
		got := strings.Join(callsFor(tc.stage, false, false, LookCalls), ",")
		if has := strings.Contains(got, CallGrep); has != tc.want {
			t.Errorf("%s may call %s: got %v, want %v (calls = %s)", tc.stage, CallGrep, has, tc.want, got)
		}
	}
}

// A lookup answers with what the tree said and records nothing, so it neither
// completes a pass nor counts toward one -- the same bargain get_context makes.
func TestALookupAnswersWithTheTreeAndRecordsNothing(t *testing.T) {
	look := &fakeLooker{answer: "store.go:3-5\n3\tfunc Insert() error {\n"}
	c := newCollector(StageFindings, passExpect{}, nil, look, false)
	input, err := json.Marshal(map[string]any{"path": "store.go", "start_line": 3, "end_line": 5})
	if err != nil {
		t.Fatal(err)
	}
	results, done, rejected := c.take([]toolCall{{ID: "t1", Name: CallRead, Input: input}})
	if rejected != 0 || done {
		t.Fatalf("rejected=%d done=%v; a read is neither refused nor an ending", rejected, done)
	}
	if len(results) != 1 || results[0].isError {
		t.Fatalf("results = %+v, want the span back", results)
	}
	if !strings.Contains(results[0].content, "func Insert") {
		t.Errorf("content = %q, want what the tree said", results[0].content)
	}
	if len(look.reads) != 1 || look.reads[0] != "store.go" {
		t.Errorf("reads = %v, want the path as asked", look.reads)
	}
	if c.looked != 1 {
		t.Errorf("looked = %d, want the lookup counted for the trace", c.looked)
	}
}

// A failed lookup is an answer, not a rejection. "Not recorded" reads as a call
// this pass may not make, which is a different and wrong correction.
func TestAFailedLookupComesBackAsTheAnswer(t *testing.T) {
	look := &fakeLooker{err: errBadPattern{}}
	c := newCollector(StageFindings, passExpect{}, nil, look, false)
	input, err := json.Marshal(map[string]any{"pattern": "("})
	if err != nil {
		t.Fatal(err)
	}
	results, _, rejected := c.take([]toolCall{{ID: "t1", Name: CallGrep, Input: input}})
	if rejected != 0 {
		t.Errorf("rejected = %d, want the error carried as the answer", rejected)
	}
	if len(results) != 1 || results[0].isError {
		t.Fatalf("results = %+v, want a plain answer", results)
	}
	if !strings.Contains(results[0].content, "bad pattern") {
		t.Errorf("content = %q, want the reason the pass can act on", results[0].content)
	}
}

type errBadPattern struct{}

func (errBadPattern) Error() string { return "bad pattern: missing closing )" }

// A search that matched nothing says so. A pass handed an empty string cannot
// tell "nothing matches" -- which is an answer -- from a search that failed.
func TestAnEmptySearchSaysSo(t *testing.T) {
	c := newCollector(StageFindings, passExpect{}, nil, &fakeLooker{answer: ""}, false)
	input, err := json.Marshal(map[string]any{"pattern": "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	results, _, _ := c.take([]toolCall{{ID: "t1", Name: CallGrep, Input: input}})
	if len(results) != 1 || !strings.Contains(results[0].content, "no match") {
		t.Fatalf("results = %+v, want an explicit no-match", results)
	}
}

// The findings pass can see set_overview and describe_file on the catalogue,
// because the tool list is frozen for the run so every call reads one cached
// prefix. Told only that it takes add_comment, a pass has reached for them and
// rewritten a walkthrough that already existed. The instruction has to say the
// work is done, since the catalogue cannot say it.
func TestTheFindingsPassIsToldTheWalkthroughIsWritten(t *testing.T) {
	block := callsBlock(StageFindings, false, false, nil)
	if !strings.Contains(block, "already written") {
		t.Errorf("the findings pass is not told the walkthrough exists:\n%s", block)
	}
	for _, name := range []string{CallOverview, CallFile} {
		if !strings.Contains(block, name) {
			t.Errorf("the findings pass is not told %s is not its to make:\n%s", name, block)
		}
	}
	// The describing pass must not be told its own work is already done.
	if syn := callsBlock(StageSynopsis, false, false, nil); strings.Contains(syn, "already written") {
		t.Errorf("the describing pass is told its work is done:\n%s", syn)
	}
}

// The judging call is handed the walkthrough the describing call wrote.
// Telling it one exists has been tried twice and did not hold: the pass was
// asked to believe in something it could not see while the catalogue still
// offered the tools to make one. Showing it is what settles that.
func TestTheJudgingCallCarriesTheWalkthrough(t *testing.T) {
	w := findings.Review{
		Overview: "This change batches the recompute.",
		Files: map[string]string{
			"b.go": "second file",
			"a.go": "first file",
		},
	}
	res := &Result{Prompt: "material", Tail: "old tail"}

	got := res.judgingRequest(w, false).Tail
	for _, want := range []string{"batches the recompute", "a.go: first file", "b.go: second file"} {
		if !strings.Contains(got, want) {
			t.Errorf("the judging tail is missing %q:\n%s", want, got)
		}
	}
	// Sorted, because Files is a map and an unordered tail would make two
	// runs of one change two different requests.
	if strings.Index(got, "a.go") > strings.Index(got, "b.go") {
		t.Errorf("the file lines are not sorted:\n%s", got)
	}
	// It is material, so it precedes the instruction the pass acts on.
	if i, j := strings.Index(got, "already written"), strings.Index(got, "Finding defects"); i < 0 || j < 0 || i > j {
		t.Errorf("the walkthrough must come before the judging instruction:\n%s", got)
	}

	// The fallback writes its own walkthrough, so it is not handed one.
	if fb := res.judgingRequest(findings.Review{}, true).Tail; strings.Contains(fb, "already written") {
		t.Errorf("the fallback pass must not be told a walkthrough exists:\n%s", fb)
	}
	// Nothing to show means nothing is added.
	if empty := res.judgingRequest(findings.Review{}, false).Tail; strings.Contains(empty, "already written") {
		t.Errorf("an empty walkthrough must add nothing:\n%s", empty)
	}
}

// The ledger records the effort the calls were made at, not the flag that was
// typed. Those differ on every run that takes the default, which is the whole
// population a default exists to describe: reading the flag wrote an empty
// string for all of them and the ledger could not tell two efforts apart.
func TestTheLedgerRecordsTheResolvedEffort(t *testing.T) {
	in := Input{Change: &change.Set{Files: []change.File{{Path: "a.go", Diff: "@@ -1 +1 @@\n+x\n"}}}}
	res, err := Assemble(in, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Effort != DefaultEffort {
		t.Errorf("effort = %q, want the resolved default %q", res.Effort, DefaultEffort)
	}
	dir := t.TempDir()
	if err := Record(dir, res); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadLedger(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %d, err = %v", len(entries), err)
	}
	if entries[0].Effort != DefaultEffort {
		t.Errorf("the ledger row says effort %q, want %q", entries[0].Effort, DefaultEffort)
	}
	// An asked-for effort still wins.
	asked, err := Assemble(in, Options{Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if asked.Effort != "high" {
		t.Errorf("effort = %q, want high", asked.Effort)
	}
}

// The trace says what the lookups asked and what the refused calls were, not
// only how many there were of each. A count records that something happened
// sixteen times and nothing about what, which is the shape the saved artifact
// was in when a pass on PR #1462 had thirteen calls refused.
func TestThePassRecordsWhatItLookedUpAndWhatWasRefused(t *testing.T) {
	look := &fakeLooker{answer: "store.go:3: func Insert() error {"}
	c := newCollector(StageFindings, passExpect{}, nil, look, false)

	grep, err := json.Marshal(map[string]any{"pattern": "func Insert", "glob": ".go"})
	if err != nil {
		t.Fatal(err)
	}
	c.take([]toolCall{{ID: "t1", Name: CallGrep, Input: grep}})

	if len(c.lookups) != 1 {
		t.Fatalf("lookups = %+v, want the one call recorded", c.lookups)
	}
	got := c.lookups[0]
	if got.Tool != CallGrep || !strings.Contains(got.Input, "func Insert") {
		t.Errorf("lookup = %+v, want the tool and what it asked", got)
	}
	if got.Bytes == 0 || got.Empty {
		t.Errorf("lookup = %+v, want the yield of an answer that carried something", got)
	}

	// A search that found nothing is an answer, and is worth telling apart.
	c2 := newCollector(StageFindings, passExpect{}, nil, &fakeLooker{answer: ""}, false)
	c2.take([]toolCall{{ID: "t1", Name: CallGrep, Input: grep}})
	if len(c2.lookups) != 1 || !c2.lookups[0].Empty {
		t.Errorf("lookups = %+v, want the empty answer marked", c2.lookups)
	}

	// A call this pass may not make is recorded with the reason, and the same
	// mistake twice is one entry counted twice rather than two entries.
	over, err := json.Marshal(map[string]any{"overview": "the change does a thing"})
	if err != nil {
		t.Fatal(err)
	}
	c.take([]toolCall{{ID: "t2", Name: CallOverview, Input: over}})
	c.take([]toolCall{{ID: "t3", Name: CallOverview, Input: over}})
	if len(c.refusals) != 1 {
		t.Fatalf("refusals = %+v, want one reason", c.refusals)
	}
	if r := c.refusals[0]; r.Count != 2 || r.Tool != CallOverview || !strings.Contains(r.Why, "does not take") {
		t.Errorf("refusal = %+v, want the reason counted twice", r)
	}
}

// Every message a lookup uses to say it found nothing counts as empty. The
// trace's whole value is telling ground that was checked and came back bare
// from ground that came back with something, and a lookup that says so in its
// own words rather than returning "" must not be counted as a full answer.
func TestEveryNothingFoundMessageCountsAsEmpty(t *testing.T) {
	for _, s := range []string{
		"no match in this repository, which is the whole of what this searches",
		"no documents in this repository",
		"no documents under docs/adr/",
		"gorefactor knows no symbol by that name",
		"no recorded history for those lines",
		"these lines have no history before this change; 3 commit(s) touching them are the change itself",
		"",
		"   \n",
	} {
		if !emptyAnswer(s) {
			t.Errorf("not counted as empty: %q", s)
		}
	}
	for _, s := range []string{
		"store.go:3: func Insert() error {",
		"commit abc123\n    add the guard\n",
	} {
		if emptyAnswer(s) {
			t.Errorf("wrongly counted as empty: %q", s)
		}
	}
}

// Every tool's schema says required is an array, even when nothing is
// required. A variadic with no arguments is a nil slice and marshals to
// `null`: the Anthropic wire tolerates that and the OpenAI wire refuses the
// whole call with "None is not of type 'array'", so a tool with no required
// field went out broken on the wire nothing here is usually pointed at.
func TestEveryToolSchemaHasAnArrayOfRequiredFields(t *testing.T) {
	raw, err := json.Marshal(callTools(true, true, true, LookCalls))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"required":null`)) {
		t.Errorf("a tool schema carries a null required:\n%s", raw)
	}
	var tools []struct {
		Name   string `json:"name"`
		Schema struct {
			Required *[]string `json:"required"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if tl.Schema.Required == nil {
			t.Errorf("%s: required is null, want an array", tl.Name)
		}
	}
}

// The judging catalogue carries the three describing tools only where the two
// calls share a prefix. Sent to another model or another wire, a cache entry
// belongs to whoever wrote it, there is no prefix to match, and the tools come
// off: which is why a findings pass cannot be asked to write a walkthrough
// after the fact, and why a failed describing call stops the run.
func TestTheJudgingCatalogueNarrowsWhenThePrefixIsNotShared(t *testing.T) {
	shared := Options{Synopsis: true, Cache: true}.withDefaults()
	if !shared.describingSharesPrefix() {
		t.Fatal("the default shape shares a prefix")
	}
	if !shared.judgingCatalogueDescribes() {
		t.Error("a shared prefix keeps the describing tools on the catalogue, so a fallback can write one")
	}

	apart := Options{Synopsis: true, Cache: true,
		Describing: Endpoint{API: "openai", Model: "gpt-5.6-luna"}}.withDefaults()
	if apart.describingSharesPrefix() {
		t.Fatal("a describing call on another wire shares no prefix")
	}
	if apart.judgingCatalogueDescribes() {
		t.Error("with no shared prefix the describing tools come off the catalogue")
	}
	// Which is why there is nothing to fall back to: the request the run would
	// have to send is one whose tools it is no longer sending.
	res := &Result{Prompt: "material"}
	if got := res.judgingRequest(findings.Review{}, false); got.Stage != StageFindings {
		t.Errorf("stage = %q, want the run to stay a findings call", got.Stage)
	}
}

// stallingConv fails the first n sends with err, then behaves.
type stallingConv struct {
	fail int
	err  error
	sent int
	last turnReply
}

func (c *stallingConv) send(_ context.Context, _ int64) (turnReply, error) {
	c.sent++
	if c.sent <= c.fail {
		// A stream that dies partway still reports what it read: message_start
		// carries the input count before any content arrives.
		return turnReply{usage: Usage{InputTokens: 100}}, c.err
	}
	return c.last, nil
}
func (c *stallingConv) answer(turnReply, []callResult) {}
func (c *stallingConv) nudge(turnReply, string)        {}

// A stalled connection is not an answer, so the turn goes out again. Without
// this a pass thirteen turns into its lookups ends with whatever it recorded,
// which is nothing, and a network blip costs the whole review.
func TestAStalledTurnIsSentAgain(t *testing.T) {
	old := sendBackoff
	sendBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { sendBackoff = old }()

	conv := &stallingConv{fail: 1, err: fmt.Errorf("no data received: %w", errStalled),
		last: turnReply{usage: Usage{InputTokens: 900}}}
	var said []string
	var total Usage
	r, err := sendTurn(context.Background(), Options{Progress: func(s string) { said = append(said, s) }},
		conv, 1000, StageFindings, 7, &total)
	if err != nil {
		t.Fatalf("a retried turn must succeed: %v", err)
	}
	if conv.sent != 2 {
		t.Errorf("sent %d time(s), want the turn tried again", conv.sent)
	}
	if r.usage.InputTokens != 900 {
		t.Errorf("the successful reply must come back: %+v", r.usage)
	}
	// The broken attempt was billed and has to stay in the accounting.
	if total.InputTokens != 100 {
		t.Errorf("total = %+v, want the failed attempt's 100 input tokens counted", total)
	}
	if len(said) != 1 || !strings.Contains(said[0], "sending it again") {
		t.Errorf("a retry must be visible: %v", said)
	}
}

// Bounded, and only for the connection. A 400 will be a 400 again, a refusal
// is an answer, and sending either a second time buys the same reply twice.
func TestOnlyABrokenConnectionIsRetried(t *testing.T) {
	old := sendBackoff
	sendBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	defer func() { sendBackoff = old }()

	var total Usage
	bad := &stallingConv{fail: 9, err: errors.New("400 Bad Request: Invalid schema")}
	if _, err := sendTurn(context.Background(), Options{}, bad, 1000, StageFindings, 1, &total); err == nil {
		t.Fatal("a bad request must come straight back")
	}
	if bad.sent != 1 {
		t.Errorf("a bad request was sent %d times, want once", bad.sent)
	}

	stalls := &stallingConv{fail: 9, err: fmt.Errorf("gone: %w", errStalled)}
	if _, err := sendTurn(context.Background(), Options{}, stalls, 1000, StageFindings, 1, &total); err == nil {
		t.Fatal("a connection that never comes back must still fail")
	}
	if stalls.sent != sendAttempts {
		t.Errorf("sent %d time(s), want the bound of %d", stalls.sent, sendAttempts)
	}
}

func TestConnectionBrokeNamesTheNetworkOnly(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("wrapped: %w", errStalled),
		io.ErrUnexpectedEOF,
		syscall.ECONNRESET,
		syscall.EPIPE,
	} {
		if !connectionBroke(err) {
			t.Errorf("not treated as the connection: %v", err)
		}
	}
	for _, err := range []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		errors.New("400 Bad Request"),
		errors.New("the model declined this request"),
	} {
		if connectionBroke(err) {
			t.Errorf("wrongly treated as the connection: %v", err)
		}
	}
}

// read_lines asks for two integers and keeps being given a string. Across four
// runs the same field arrived as "70, 145", "20-1", "412/-" and "20, \n": a
// line number, then whatever the model was still thinking. Refusing was the
// honest answer while nothing else was available and it did not work - the
// message says "want an integer, got the string" and the string comes back
// differently mangled next time, at a turn each.
func TestALineNumberWrittenAsAStringIsRead(t *testing.T) {
	for _, tc := range []struct {
		written string
		want    int
	}{
		{`"412"`, 412},
		{`" 412 "`, 412},
		{`"70, 145"`, 70},
		{`"20-1"`, 20},
		{`"412/-"`, 412},
		{`"20, \n"`, 20},
		{`412`, 412},
	} {
		look := &fakeLooker{answer: "a.go:1-2\n1\tx\n"}
		c := newCollector(StageFindings, passExpect{}, nil, look)
		in := json.RawMessage(`{"path":"a.go","start_line":` + tc.written + `,"end_line":430}`)
		results, _, rejected := c.take([]toolCall{{ID: "t1", Name: CallRead, Input: in}})
		if rejected != 0 {
			t.Errorf("start_line %s was refused: %s", tc.written, results[0].content)
			continue
		}
		if len(c.lookups) != 1 {
			t.Errorf("start_line %s reached no lookup", tc.written)
			continue
		}
		// The trace keeps what the model wrote, not what was read from it:
		// a trace showing the coerced value hides that coercion happened.
		if !strings.Contains(c.lookups[0].Input, strings.Trim(tc.written, `"`)[:2]) {
			t.Errorf("the trace lost the written form %s: %s", tc.written, c.lookups[0].Input)
		}
	}
}

// A string with no number at the front is still refused: there the meaning is
// genuinely unknown, and guessing at one would read the wrong lines silently.
func TestAStringWithNoNumberIsStillRefused(t *testing.T) {
	c := newCollector(StageFindings, passExpect{}, nil, &fakeLooker{})
	in := json.RawMessage(`{"path":"a.go","start_line":"the guard","end_line":430}`)
	results, _, rejected := c.take([]toolCall{{ID: "t1", Name: CallRead, Input: in}})
	if rejected != 1 {
		t.Fatalf("rejected = %d, want the unreadable value refused", rejected)
	}
	if !strings.Contains(results[0].content, "want an integer") {
		t.Errorf("the refusal must say what was wanted: %s", results[0].content)
	}
}

func TestLeadingIntegerReadsOnlyALeadingInteger(t *testing.T) {
	for in, want := range map[string]int64{"412": 412, " 412 ": 412, "70, 145": 70, "-3x": -3, "20-1": 20} {
		if got, ok := leadingInteger(in); !ok || got != want {
			t.Errorf("leadingInteger(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", "abc", "-", "+", "x412"} {
		if got, ok := leadingInteger(in); ok {
			t.Errorf("leadingInteger(%q) = %d, true; want no number", in, got)
		}
	}
}
