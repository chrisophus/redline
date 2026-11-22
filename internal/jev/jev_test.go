package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

func client(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "", "k")
	if err != nil {
		t.Fatal(err)
	}
	c.wait = func(int, string) time.Duration { return 0 }
	return c
}

// The request and reply are the shapes TypeSafe documents: questions keyed by
// a name the caller chose, and one probability back under each name.
func TestAskSendsNoulsAndReadsProbabilities(t *testing.T) {
	var got struct {
		Model     string `json:"model"`
		State     map[string]string
		Questions map[string]struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     map[string]string `json:"criteria"`
		} `json:"questions"`
	}
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/systemone" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path %q auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"model":"jev-1.14.0","answers":{"a":{"type":"noul","noul":0.93},"b":{"type":"noul","noul":0.1}},
			"usage":{"input_tokens":1200,"output_tokens":2}}`)
	})
	ans, usage, err := c.Ask(context.Background(), map[string]string{"text": "hello"}, map[string]Noul{
		"a": {Instructions: "Is it a greeting?", True: "yes it is", False: "no"},
		"b": {Instructions: "Is it a question?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans["a"] != 0.93 || ans["b"] != 0.1 {
		t.Errorf("answers = %v", ans)
	}
	if cost := usage.CostUSD(); usage.InputTokens != 1200 || cost < 0.00005 || cost > 0.000051 {
		t.Errorf("usage = %+v cost %v", usage, usage.CostUSD())
	}
	if got.Model != DefaultModel || got.State["text"] != "hello" {
		t.Errorf("request = %+v", got)
	}
	if q := got.Questions["a"]; q.Type != "noul" || q.Criteria["true"] != "yes it is" {
		t.Errorf("question a = %+v", q)
	}
	if q := got.Questions["b"]; q.Criteria != nil {
		t.Errorf("a question with no criteria sent some: %+v", q)
	}
}

func TestAskRetriesWhenTheEndpointIsBusy(t *testing.T) {
	n := 0
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"answers":{"a":{"noul":0.5}},"usage":{"input_tokens":1}}`)
	})
	ans, _, err := c.Ask(context.Background(), "s", map[string]Noul{"a": {Instructions: "q"}})
	if err != nil || ans["a"] != 0.5 || n != 3 {
		t.Fatalf("ans=%v err=%v after %d request(s)", ans, err, n)
	}
}

// A wrong key is not something a retry fixes, and the server's words are the
// only explanation the caller gets.
func TestAskQuotesARefusal(t *testing.T) {
	n := 0
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		n++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"invalid api key"}`)
	})
	_, _, err := c.Ask(context.Background(), "s", map[string]Noul{"a": {Instructions: "q"}})
	if err == nil || !strings.Contains(err.Error(), "invalid api key") || n != 1 {
		t.Fatalf("err=%v after %d request(s)", err, n)
	}
}

func TestNewNeedsAKey(t *testing.T) {
	if _, err := New("", "", ""); err == nil || !strings.Contains(err.Error(), KeyEnv) {
		t.Fatalf("err = %v", err)
	}
}

// answerAll replies to every question in a request with what score says for
// its name, and counts the requests.
func answerAll(t *testing.T, n *int, seen *map[string]json.RawMessage, state *json.RawMessage, score func(name string) float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*n++
		var req struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Error(err)
		}
		if seen != nil {
			*seen = req.Questions
		}
		if state != nil {
			*state = req.State
		}
		ans := map[string]map[string]float64{}
		for name := range req.Questions {
			ans[name] = map[string]float64{"noul": score(name)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans, "usage": map[string]int{"input_tokens": 700}})
	}
}

// One request per finding. The state holds the finding alone, and each
// earlier comment rides in the questions asked about it, so no question is
// shown a comment that belongs to another.
func TestRepeatsAsksAboutEachEarlierCommentOnItsOwn(t *testing.T) {
	var questions map[string]json.RawMessage
	var state json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(name string) float64 {
		switch {
		case strings.HasPrefix(name, "code0"):
			return 0.2
		case strings.HasPrefix(name, "fail0"):
			return 0.4
		case strings.HasPrefix(name, "code1"):
			return 0.9
		}
		return 1.0
	}))
	r := &Repeats{Client: c}
	got, err := r.Same(context.Background(),
		findings.ReviewComment{File: "a.go", Line: 9, Body: "the name check is case-sensitive"},
		[]feedback.Thread{{File: "b.go", Said: "**Warning · agent** — the lock is held"}, {File: "a.go", Line: 7, Said: "names compare by exact case"}})
	if err != nil {
		t.Fatal(err)
	}
	// The mean of the two questions.
	if len(got) != 2 || got[0] < 0.299 || got[0] > 0.301 || got[1] < 0.949 || got[1] > 0.951 {
		t.Fatalf("got %v", got)
	}
	if want := `{"new_comment":{"file":"a.go","line":9,"text":"the name check is case-sensitive"}}`; string(state) != want {
		t.Errorf("the state should hold the finding and nothing else: %s", state)
	}
	if len(questions) != 2*2*copies {
		t.Fatalf("want two questions asked %d times per earlier comment, got %d", copies, len(questions))
	}
	// The question comes before the comment it is about, and yes before no.
	// Written the other way round, the same pairs scored up to 0.2 lower.
	q := string(questions["fail1.0"])
	for _, order := range [][2]string{{`"question"`, `"earlier_comment"`}, {`"true"`, `"false"`}} {
		if a, b := strings.Index(q, order[0]), strings.Index(q, order[1]); a < 0 || b < a {
			t.Errorf("%s should come before %s in %s", order[0], order[1], q)
		}
	}
	if !strings.Contains(q, "names compare by exact case") || !strings.Contains(q, "same failure") {
		t.Errorf("fail1 = %s", q)
	}
	if q := string(questions["code0.0"]); !strings.Contains(q, `"text":"the lock is held"`) {
		t.Errorf("code0 should carry its comment without the display label: %s", q)
	}
	if usage, calls := r.Spent(); usage.InputTokens != 700 || calls != 1 {
		t.Errorf("spent %+v in %d", usage, calls)
	}
	if none, err := r.Same(context.Background(), findings.ReviewComment{}, nil); len(none) != 0 || err != nil {
		t.Errorf("nothing to compare should ask nothing: %v %v", none, err)
	}
}

// An answer that did not come back must not read as "not a repeat".
func TestRepeatsRefusesAPartialReply(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"answers":{"code0.0":{"noul":0.9}},"usage":{"input_tokens":10}}`)
	})
	r := &Repeats{Client: c}
	if _, err := r.Same(context.Background(), findings.ReviewComment{Body: "x"}, []feedback.Thread{{Said: "y"}}); err == nil {
		t.Fatal("a reply missing an answer was read as an answer")
	}
}

// A long pull request is asked about in several requests.
func TestRepeatsSplitsALongList(t *testing.T) {
	n := 0
	c := client(t, answerAll(t, &n, nil, nil, func(string) float64 { return 0.1 }))
	r := &Repeats{Client: c}
	earlier := make([]feedback.Thread, perRequest+3)
	for i := range earlier {
		earlier[i] = feedback.Thread{Said: "x"}
	}
	got, err := r.Same(context.Background(), findings.ReviewComment{Body: "y"}, earlier)
	if err != nil || len(got) != len(earlier) || n != 2 {
		t.Fatalf("got %d answers in %d request(s): %v", len(got), n, err)
	}
}

// Each judge sends one item per question, asked copies times, and returns
// the average for each item.
func TestMissesAsksAboutEachFindingOnItsOwn(t *testing.T) {
	var questions map[string]json.RawMessage
	var state json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(name string) float64 {
		if strings.HasPrefix(name, "q1.") {
			return 0.8
		}
		return 0.2
	}))
	j := &Misses{Client: c}
	got, err := j.Describes(context.Background(), Defect{File: "a.go", Line: 4, Summary: "index into an empty list"},
		[]Said{{File: "b.go", Text: "the lock is held"}, {File: "a.go", Line: 6, Text: "users[0] with no length check"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] < 0.199 || got[0] > 0.201 || got[1] < 0.799 || got[1] > 0.801 {
		t.Fatalf("got %v", got)
	}
	if want := `{"defect":{"file":"a.go","line":4,"summary":"index into an empty list"}}`; string(state) != want {
		t.Errorf("state = %s", state)
	}
	if len(questions) != 2*copies {
		t.Fatalf("want each finding asked about %d times, got %d questions", copies, len(questions))
	}
	q := string(questions["q1.0"])
	if !strings.Contains(q, `"finding":{"file":"a.go","line":6,"text":"users[0] with no length check"}`) || strings.Contains(q, "the lock is held") {
		t.Errorf("a question should carry its own finding and no other: %s", q)
	}
	if usage, calls := j.Spent(); usage.InputTokens != 700 || calls != 1 {
		t.Errorf("spent %+v in %d", usage, calls)
	}
}

func TestEvidenceSendsOneClaimAsTheState(t *testing.T) {
	var questions map[string]json.RawMessage
	var state json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(string) float64 { return 0.6 }))
	j := &Evidence{Client: c}
	p, err := j.Supports(context.Background(), Claim{Finding: Said{File: "a.go", Text: "never closed"}, Evidence: "f, _ := os.Open(p)"})
	if err != nil || p < 0.599 || p > 0.601 {
		t.Fatalf("Supports = %v, %v", p, err)
	}
	if !strings.Contains(string(state), `"evidence":"f, _ := os.Open(p)"`) || !strings.Contains(string(state), "never closed") {
		t.Errorf("state = %s", state)
	}
	if len(questions) != copies {
		t.Errorf("want the question asked %d times, got %d", copies, len(questions))
	}
}

func TestContextCutsToFitAndBatches(t *testing.T) {
	var state json.RawMessage
	var questions map[string]json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(string) float64 { return 0.5 }))
	j := &Context{Client: c}
	entries := make([]Entry, contextPerRequest+1)
	for i := range entries {
		entries[i] = Entry{Role: "caller", Symbol: "F", Content: strings.Repeat("é", contextEntryChars)}
	}
	got, err := j.Needed(context.Background(),
		[]Changed{{Path: "a.go", Added: 1, Diff: strings.Repeat("x", contextDiffPerFile*2)}}, entries)
	if err != nil || len(got) != len(entries) {
		t.Fatalf("Needed = %d scores, %v", len(got), err)
	}
	if n != 2 {
		t.Errorf("%d entries went in %d requests, want 2", len(entries), n)
	}
	var st struct {
		Change []Changed `json:"change"`
	}
	if err := json.Unmarshal(state, &st); err != nil || len(st.Change[0].Diff) != contextDiffPerFile {
		t.Errorf("diff sent at %d bytes, want it cut to %d (%v)", len(st.Change[0].Diff), contextDiffPerFile, err)
	}
	// The last request holds the one entry left over. Its content is cut to
	// the limit without splitting a two-byte character.
	var q struct {
		Instructions struct {
			Entry Entry `json:"context_entry"`
		} `json:"instructions"`
	}
	for _, raw := range questions {
		if err := json.Unmarshal(raw, &q); err != nil {
			t.Fatal(err)
		}
		break
	}
	if got := len(q.Instructions.Entry.Content); got > contextEntryChars || got%2 != 0 {
		t.Errorf("entry content sent at %d bytes", got)
	}
}

func TestJudgeRefusesAPartialReply(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"answers":{"q0.0":{"noul":0.9}},"usage":{"input_tokens":10}}`)
	})
	if _, err := (&Misses{Client: c}).Describes(context.Background(), Defect{Summary: "x"}, []Said{{Text: "y"}}); err == nil {
		t.Error("a reply missing answers was read as a score")
	}
}

func TestRepliesAsksFourQuestionsInOneRequest(t *testing.T) {
	var questions map[string]json.RawMessage
	var state json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(name string) float64 {
		if strings.HasPrefix(name, "open.") {
			return 0.9
		}
		return 0.1
	}))
	j := &Replies{Client: c}
	got, err := j.Read(context.Background(), ReplyCase{File: "a.go", Finding: "leaks", Reply: "This remains open"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(questions) != 4*copies {
		t.Errorf("%d requests with %d questions, want one with %d", n, len(questions), 4*copies)
	}
	if got.Open < 0.899 || got.Open > 0.901 || got.Changed > 0.101 || got.Wrong > 0.101 || got.WontFix > 0.101 {
		t.Errorf("scores = %+v", got)
	}
	if want := `{"file":"a.go","finding":"leaks","reply":"This remains open"}`; string(state) != want {
		t.Errorf("state = %s", state)
	}
}

func TestFixesSendsTheCodeAndTheChange(t *testing.T) {
	var questions map[string]json.RawMessage
	var state json.RawMessage
	n := 0
	c := client(t, answerAll(t, &n, &questions, &state, func(string) float64 { return 0.8 }))
	p, err := (&Fixes{Client: c}).Fixed(context.Background(), FixCase{File: "a.go", Finding: "f", CodeBefore: "x := 1", Change: "+x := 2"})
	if err != nil || p < 0.799 || p > 0.801 {
		t.Fatalf("Fixed = %v, %v", p, err)
	}
	if want := `{"file":"a.go","finding":"f","code_before":"x := 1","change":"+x := 2"}`; string(state) != want {
		t.Errorf("state = %s", state)
	}
	if len(questions) != copies {
		t.Errorf("%d questions, want %d", len(questions), copies)
	}
}

func TestClipTailKeepsNewestReply(t *testing.T) {
	got := clipTail("old reply, new reply", 9)
	if got != "new reply" {
		t.Errorf("clipTail = %q, want the end of the string", got)
	}
	if got := clipTail("aé", 2); got != "é" {
		t.Errorf("clipTail split a character: %q", got)
	}
	if got := clipTail("short", 10); got != "short" {
		t.Errorf("clipTail changed a short string: %q", got)
	}
}
