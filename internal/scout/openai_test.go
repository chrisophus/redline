package scout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The OpenAI wire runs the same loop as the Anthropic one: the model calls
// tools, this program dispatches them and reads the bytes, and done ends the
// run. A stub endpoint that records a range and then finishes proves the
// function-calling round trip end to end, so a review on --api openai gets its
// lookups instead of nothing.
func TestDriveOpenAIRecordsThroughFunctionCalls(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "foo.go"), []byte("package foo\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var turn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			args, _ := json.Marshal(map[string]any{
				"role": "enclosing", "file": "foo.go",
				"start_line": 1, "end_line": 3, "symbol": "Foo", "found_via": "read",
			})
			writeOAToolCall(w, "record", string(args), "call_1")
			return
		}
		writeOAToolCall(w, "done", `{"notes":[]}`, "call_2")
	}))
	defer srv.Close()

	env, spend, err := Run(context.Background(), Options{
		Root:    root,
		Changed: []string{"foo.go"},
		Diff:    "--- foo.go\n+func Foo() {}",
		API:     "openai",
		BaseURL: srv.URL,
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spend.Turns != 2 || spend.Records != 1 {
		t.Fatalf("turns=%d records=%d, want 2/1", spend.Turns, spend.Records)
	}
	if len(env.Expansions) != 1 {
		t.Fatalf("want one expansion, got %d", len(env.Expansions))
	}
	if got := env.Expansions[0]; string(got.Role) != "enclosing" || got.File != "foo.go" {
		t.Fatalf("unexpected expansion: %+v", got)
	}
}

// A tool the model does not know is dispatched to nothing and comes back as an
// error result the model can correct from, rather than ending the run.
func TestDriveOpenAIReportsAnUnknownTool(t *testing.T) {
	root := t.TempDir()
	var turn int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn++
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			writeOAToolCall(w, "not_a_tool", `{}`, "call_1")
			return
		}
		writeOAToolCall(w, "done", `{"notes":["nothing found"]}`, "call_2")
	}))
	defer srv.Close()

	_, spend, err := Run(context.Background(), Options{
		Root: root, Changed: []string{"x"}, Diff: "d",
		API: "openai", BaseURL: srv.URL, APIKey: "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spend.Records != 0 {
		t.Fatalf("an unknown tool records nothing, got %d", spend.Records)
	}
	if spend.Turns != 2 {
		t.Fatalf("the run should recover and finish, turns=%d", spend.Turns)
	}
}

// Measured on a real review: eight turns of grep and read_lines, zero records,
// and the ruling then decided six findings with no answers in front of it. A
// model that spends the budget searching has read real code, and all of it is
// discarded, because nothing reaches the review except through record. The
// last turn of the budget therefore offers record and done alone and says the
// searching is over. Without that, this test gets no expansions at all.
func TestABudgetSpentSearchingStillFilesWhatItFound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "foo.go"), []byte("package foo\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var offered [][]string
	var closed []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		var names []string
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		offered = append(offered, names)
		var told bool
		for _, m := range req.Messages {
			if m.Role == "user" && m.Content == closingBrief {
				told = true
			}
		}
		closed = append(closed, told)

		w.Header().Set("Content-Type", "application/json")
		// The model keeps searching for as long as it is allowed to, which is
		// what the real one did. It files only once it is told to, and can
		// only file because record is what it is left holding.
		if !told {
			writeOAToolCall(w, "grep", `{"pattern":"func","glob":"*.go"}`, "call_s")
			return
		}
		args, _ := json.Marshal(map[string]any{
			"role": "enclosing", "file": "foo.go",
			"start_line": 1, "end_line": 3, "symbol": "Foo", "found_via": "grep",
		})
		writeOAToolCall(w, "record", string(args), "call_r")
	}))
	defer srv.Close()

	env, spend, err := Run(context.Background(), Options{
		Root: root, Changed: []string{"foo.go"}, Diff: "--- foo.go\n+func Foo() {}",
		API: "openai", BaseURL: srv.URL, APIKey: "sk-test", MaxTurns: 3,
		Questions: []Question{{ID: "c1", Ask: "is Foo called anywhere?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != 1 {
		t.Fatalf("a spent budget filed nothing: %d expansion(s) after %d turn(s)", len(env.Expansions), spend.Turns)
	}
	if got := len(offered); got != 3 {
		t.Fatalf("want the whole budget spent, got %d turn(s)", got)
	}
	if closed[0] || closed[1] {
		t.Error("the searching turns were told to stop searching")
	}
	if !closed[2] {
		t.Error("the last turn was not told the searching was over")
	}
	for _, name := range offered[2] {
		if name != recordTool && name != doneTool {
			t.Errorf("the last turn was still offered %q, so it can search instead of filing", name)
		}
	}
	if len(offered[0]) <= 2 {
		t.Errorf("the first turn should carry the search tools, got %v", offered[0])
	}
}

func writeOAToolCall(w http.ResponseWriter, name, args, id string) {
	resp := map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []map[string]any{{
					"id": id, "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
	}
	_ = json.NewEncoder(w).Encode(resp)
}
