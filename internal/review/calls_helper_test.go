package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// flatCallList turns a body in the shape the parser reads - the one the strict
// single-object forms used to carry - into the calls a pass makes to produce
// it, ending in done. Fields the old test bodies left out are filled with
// valid defaults, so a test written against an older body still exercises
// the pass it meant to.
func flatCallList(body string) []toolCall {
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		panic(fmt.Sprintf("flatCallList: %v in %s", err, body))
	}
	var calls []toolCall
	add := func(name string, in map[string]any) {
		raw, _ := json.Marshal(in)
		calls = append(calls, toolCall{ID: fmt.Sprintf("toolu_%d", len(calls)+1), Name: name, Input: raw})
	}
	str := func(m map[string]any, k, def string) string {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
		return def
	}
	if o, ok := v["overview"].(string); ok && o != "" {
		add(CallOverview, map[string]any{"overview": o})
	}
	if files, ok := v["files"].([]any); ok {
		for _, f := range files {
			m := f.(map[string]any)
			add(CallFile, map[string]any{"path": str(m, "path", ""), "summary": str(m, "summary", "")})
		}
	}
	if cohorts, ok := v["cohorts"].([]any); ok {
		for _, c := range cohorts {
			m := c.(map[string]any)
			files, _ := m["files"].([]any)
			if files == nil {
				files = []any{}
			}
			add(CallCohort, map[string]any{"name": str(m, "name", ""), "summary": str(m, "summary", ""), "files": files})
		}
	}
	if comments, ok := v["comments"].([]any); ok {
		for _, c := range comments {
			m := c.(map[string]any)
			q, _ := m["question"].(map[string]any)
			if q == nil {
				q = map[string]any{}
			}
			line, _ := m["line"].(float64)
			add(CallComment, map[string]any{
				"file": str(m, "file", ""), "line": int(line),
				"severity": str(m, "severity", "warning"), "confidence": str(m, "confidence", "high"),
				"body":          str(m, "body", ""),
				"question_kind": str(q, "kind", "diff"), "question_ask": str(q, "ask", ""),
				"question_subject": str(q, "subject", ""),
			})
		}
	}
	if rulings, ok := v["rulings"].([]any); ok {
		for _, r := range rulings {
			m := r.(map[string]any)
			add(CallRule, map[string]any{
				"finding": str(m, "finding", ""), "analysis": str(m, "analysis", ""),
				"verdict": str(m, "verdict", ""), "evidence": str(m, "evidence", ""), "why": str(m, "why", ""),
			})
		}
	}
	add(CallDone, map[string]any{})
	return calls
}

// flatCalls is flatCallList as Messages API stream blocks, for anthropicSSE.
// The stage is named at the call site for the reader; the calls do not
// depend on it.
func flatCalls(_ string, body string) string {
	var b strings.Builder
	for i, c := range flatCallList(body) {
		b.WriteString(anthropicToolUse(i, c.ID, c.Name, string(c.Input)))
	}
	return b.String()
}

// openAIFlatCalls is flatCallList as a chat completions tool_calls array.
func openAIFlatCalls(body string) []map[string]any {
	var out []map[string]any
	for _, c := range flatCallList(body) {
		out = append(out, map[string]any{
			"id": c.ID, "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": string(c.Input)},
		})
	}
	return out
}
