package review

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The review's output, as small tool calls.
//
// Every call of every review declares the same six tools, none of them strict,
// and the model answers a pass by calling them: set_overview once,
// describe_file once per file, add_comment once per finding, and so on, then
// done. Redline records each call as it arrives and answers it, so a pass is a
// short back-and-forth rather than one object written in a single silent turn.
//
// It used to be one strict tool per stage, each carrying the whole answer as
// one object, and three things pushed it here, all measured on 2026-09-16:
//
//   - Strict tools compile into one grammar with a size limit, and the forms
//     together passed it ("The compiled grammar is too large"). Which forms a
//     call could carry then depended on the run's shape, and a failed
//     describing call could not fall back to the form that writes the whole
//     review.
//   - The same single-object forms without strict came back unusable: 8 of 9
//     findings calls wrote the comments array as a string with the model's
//     own tool-call markup inside it.
//   - Flat calls without strict failed 2 times in 202 across fifteen reviews,
//     both a missing severity, and both were sent again correctly after the
//     error came back.
//
// Nothing here is strict, so there is no grammar and no limit, and the array
// is the same bytes on every call, which is what lets every call of a run read
// the prompt the first one cached. A call that fails its schema costs only
// itself: it is answered with what is wrong and the model sends it again,
// while every call before it is already recorded.

// Stages: which pass a call is. They label the request, the captured files
// and the calls a pass may make; they are not tool names.
const (
	StageReview   = "review"
	StageRuling   = "ruling"
	StageSynopsis = "synopsis"
	StageFindings = "findings"
)

// Tool names.
const (
	CallOverview = "set_overview"
	CallFile     = "describe_file"
	CallCohort   = "add_cohort"
	CallComment  = "add_comment"
	CallRule     = "rule"
	CallDone     = "done"
)

// callTool is one tool's definition as it goes on the wire.
type callTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// callTools is every tool, in a fixed order. The order is part of the bytes a
// cache read depends on, so it is written out rather than built from a map.
func callTools() []callTool {
	return []callTool{
		{CallOverview, "Set the overview of this change: what it does and why. Call once.",
			flatObject(map[string]any{"overview": overviewSchema()}, "overview")},
		{CallFile, "Record one line on what one file's change does and why. Call once per file you are asked to describe.",
			flatObject(propsOf(fileSchema(), "path", "summary"), "path", "summary")},
		{CallCohort, "Record one cohort of files best reviewed together. Call once per cohort, and only when the pass asks for cohorts.",
			flatObject(propsOf(cohortSchema(), "name", "summary", "files"), "name", "summary", "files")},
		{CallComment, "Record one review comment on the change. Call once per comment.",
			commentCallSchema()},
		{CallRule, "Record the ruling on one finding. Call once per finding you were given.",
			flatObject(propsOf(rulingItemSchema(), "finding", "analysis", "verdict", "evidence", "why"),
				"finding", "analysis", "verdict", "evidence", "why")},
		{CallDone, "Call once every other call this pass asks for has been made. It ends the pass.",
			map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
	}
}

// commentCallSchema is a comment with its question flattened into three
// fields. A nested object is where an unconstrained call is most likely to go
// wrong, and flat fields cost nothing to put back together.
func commentCallSchema() map[string]any {
	c := commentSchema()
	props := propsOf(c, "file", "line", "severity", "confidence", "body")
	q := c["properties"].(map[string]any)["question"].(map[string]any)["properties"].(map[string]any)
	props["question_kind"], props["question_ask"], props["question_subject"] = q["kind"], q["ask"], q["subject"]
	return flatObject(props, "file", "line", "severity", "confidence", "body",
		"question_kind", "question_ask", "question_subject")
}

func propsOf(schema map[string]any, keys ...string) map[string]any {
	props := schema["properties"].(map[string]any)
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = props[k]
	}
	return out
}

func flatObject(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": props,
	}
}

// callsFor is which tools a pass may call, besides done. A describing pass
// that files a comment has done the next pass's job without its instruction,
// so the call is refused rather than quietly recorded.
func callsFor(stage string) []string {
	switch stage {
	case StageSynopsis:
		return []string{CallOverview, CallFile, CallCohort}
	case StageFindings:
		return []string{CallComment}
	case StageRuling:
		return []string{CallRule}
	}
	return []string{CallOverview, CallFile, CallComment}
}

// callsBlock tells a pass which calls answer it. It goes after the cached
// prompt and ahead of the pass's own instruction, so every pass of a run reads
// the same cache entry and each is told only what it may call.
func callsBlock(stage string) string {
	var b strings.Builder
	b.WriteString("\n\n## How to answer\n\nAnswer with tool calls, not prose. Make as many calls in one reply as you can, and call done once every call is made. A call that is rejected comes back saying why; send it again fixed.\n\n")
	switch stage {
	case StageSynopsis:
		b.WriteString("- set_overview once\n- describe_file once per file on the list\n- add_cohort once per cohort, only if this pass asks for cohorts\n\nMake no other call.\n")
	case StageFindings:
		b.WriteString("- add_comment once per comment\n\nMake no other call. If there is nothing to say, call done alone.\n")
	case StageRuling:
		b.WriteString("- rule once per finding you were given\n\nMake no other call: the overview and the file lines are already written, and this pass only rules.\n")
	default:
		b.WriteString("- set_overview once\n- describe_file once per file you were shown\n- add_comment once per comment\n\nMake no other call. If there is nothing to comment on, make no add_comment call.\n")
	}
	return b.String()
}

// collector records a pass's calls as they arrive and assembles the object
// the rest of the review reads, the same one the strict forms used to carry.
type collector struct {
	stage    string
	overview string
	files    []map[string]any
	cohorts  []map[string]any
	comments []map[string]any
	rulings  []map[string]any
	schemas  map[string]map[string]any
	rejected int
}

func newCollector(stage string) *collector {
	schemas := map[string]map[string]any{}
	for _, t := range callTools() {
		schemas[t.Name] = t.Schema
	}
	return &collector{stage: stage, schemas: schemas}
}

// any reports whether anything but done has been recorded.
func (c *collector) any() bool {
	return c.overview != "" || len(c.files) > 0 || len(c.cohorts) > 0 ||
		len(c.comments) > 0 || len(c.rulings) > 0
}

// take records one reply's calls and says what to answer each with. done is
// true when the reply called done and nothing in it was rejected: a done
// beside a rejected call ends nothing, because the call it rejected is one the
// model still owes.
func (c *collector) take(calls []toolCall) (results []callResult, done bool, rejected int) {
	sawDone := false
	var doneIDs []string
	for _, call := range calls {
		if call.Name != CallDone && !contains(callsFor(c.stage), call.Name) {
			// Not a malformed call but one this pass has no use for, so it is
			// not to be sent again.
			rejected++
			results = append(results, callResult{id: call.ID, isError: true,
				content: fmt.Sprintf("Not recorded: this pass does not take %s; it takes %s. Do not send it again.",
					call.Name, strings.Join(callsFor(c.stage), ", "))})
			continue
		}
		problems := c.check(call)
		if len(problems) > 0 {
			rejected++
			results = append(results, callResult{id: call.ID, isError: true,
				content: "Not recorded: " + strings.Join(problems, "; ") + ". Send this call again with that fixed."})
			continue
		}
		if call.Name == CallDone {
			sawDone = true
			doneIDs = append(doneIDs, call.ID)
			continue
		}
		c.record(call)
		results = append(results, callResult{id: call.ID, content: "Recorded."})
	}
	c.rejected += rejected
	for _, id := range doneIDs {
		switch {
		case rejected > 0:
			results = append(results, callResult{id: id, isError: true,
				content: fmt.Sprintf("Not done: %d call(s) in this reply were rejected. Send them again fixed, then call done.", rejected)})
		case c.missing() != "":
			results = append(results, callResult{id: id, isError: true, content: "Not done: " + c.missing() + "."})
			rejected++
			c.rejected++
		default:
			results = append(results, callResult{id: id, content: "Done."})
		}
	}
	return results, sawDone && rejected == 0, rejected
}

// missing is what a pass has to have recorded before done can end it, and
// empty when nothing is. A walkthrough with no overview is the one reply the
// review cannot use, so it is asked for rather than accepted.
func (c *collector) missing() string {
	switch c.stage {
	case StageSynopsis, StageReview:
		if c.overview == "" {
			return "set_overview has not been called"
		}
	}
	return ""
}

// check returns what is wrong with a call, or nothing.
func (c *collector) check(call toolCall) []string {
	schema, ok := c.schemas[call.Name]
	if !ok {
		return []string{fmt.Sprintf("there is no tool named %q", call.Name)}
	}
	var in any
	if len(call.Input) == 0 {
		in = map[string]any{}
	} else if err := json.Unmarshal(call.Input, &in); err != nil {
		return []string{"the input is not a JSON object: " + err.Error()}
	}
	return validate(schema, in, call.Name)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (c *collector) record(call toolCall) {
	var in map[string]any
	_ = json.Unmarshal(call.Input, &in)
	// A call sent again replaces the one it repeats rather than adding a
	// second. A model whose done was refused resends what it already sent as
	// often as it sends only what was missing.
	switch call.Name {
	case CallOverview:
		c.overview, _ = in["overview"].(string)
	case CallFile:
		c.files = upsert(c.files, in, "path")
	case CallCohort:
		c.cohorts = upsert(c.cohorts, in, "name")
	case CallComment:
		comment := map[string]any{}
		for _, k := range []string{"file", "line", "severity", "confidence", "body"} {
			comment[k] = in[k]
		}
		comment["question"] = map[string]any{
			"kind": in["question_kind"], "ask": in["question_ask"], "subject": in["question_subject"],
		}
		for _, have := range c.comments {
			if have["file"] == comment["file"] && have["line"] == comment["line"] && have["body"] == comment["body"] {
				return
			}
		}
		c.comments = append(c.comments, comment)
	case CallRule:
		c.rulings = upsert(c.rulings, in, "finding")
	}
}

// upsert replaces the entry whose key field matches, or appends.
func upsert(list []map[string]any, in map[string]any, key string) []map[string]any {
	for i, have := range list {
		if have[key] == in[key] {
			list[i] = in
			return list
		}
	}
	return append(list, in)
}

// body is what the pass recorded, as the object the parser reads.
func (c *collector) body() string {
	list := func(v []map[string]any) []map[string]any {
		if v == nil {
			return []map[string]any{}
		}
		return v
	}
	var v map[string]any
	switch c.stage {
	case StageSynopsis:
		v = map[string]any{"overview": c.overview, "files": list(c.files), "cohorts": list(c.cohorts)}
	case StageFindings:
		v = map[string]any{"comments": list(c.comments)}
	case StageRuling:
		v = map[string]any{"rulings": list(c.rulings)}
	default:
		v = map[string]any{"overview": c.overview, "files": list(c.files), "comments": list(c.comments)}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// progress is the per-turn line: what this pass has recorded so far.
func (c *collector) progress() string {
	switch c.stage {
	case StageSynopsis:
		return fmt.Sprintf("%d file line(s), %d cohort(s)", len(c.files), len(c.cohorts))
	case StageRuling:
		return fmt.Sprintf("%d ruling(s)", len(c.rulings))
	case StageFindings:
		return fmt.Sprintf("%d comment(s)", len(c.comments))
	}
	return fmt.Sprintf("%d file line(s), %d comment(s)", len(c.files), len(c.comments))
}

// validate checks v against the part of JSON Schema these tools use: type,
// properties, required, additionalProperties false, enum and items. Each
// problem names the field, so the model can be told exactly what to fix.
func validate(s map[string]any, v any, path string) []string {
	var errs []string
	if enum, ok := s["enum"].([]string); ok {
		str, _ := v.(string)
		if !contains(enum, str) {
			errs = append(errs, fmt.Sprintf("%s: %v is not one of %s", path, v, strings.Join(enum, ", ")))
		}
	}
	switch s["type"] {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: want an object, got %s", path, jsonKind(v)))
		}
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]string); ok {
			for _, r := range req {
				if _, ok := obj[r]; !ok {
					errs = append(errs, fmt.Sprintf("%s: missing required %s", path, r))
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if s["additionalProperties"] == false {
					errs = append(errs, fmt.Sprintf("%s: unexpected field %s", path, k))
				}
				continue
			}
			errs = append(errs, validate(ps, obj[k], path+"."+k)...)
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return append(errs, fmt.Sprintf("%s: want an array, got %s", path, jsonKind(v)))
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, it := range arr {
				errs = append(errs, validate(items, it, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			errs = append(errs, fmt.Sprintf("%s: want a string, got %s", path, jsonKind(v)))
		}
	case "integer":
		if f, ok := v.(float64); !ok || f != float64(int64(f)) {
			errs = append(errs, fmt.Sprintf("%s: want an integer, got %s", path, jsonKind(v)))
		}
	}
	return errs
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return fmt.Sprintf("the string %q", truncateForError(x))
	case float64:
		return fmt.Sprintf("the number %v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func truncateForError(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
