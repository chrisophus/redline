package review

import (
	"encoding/json"
	"fmt"
	"slices"
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
	CallGrep     = "grep"
	CallRead     = "read_lines"
	CallDocs     = "list_docs"
	CallSymbol   = "symbol_context"
	CallHistory  = "line_history"
	CallDone     = "done"
)

// LookCalls is every call a Looker can serve, in catalogue order. A run offers
// the subset its Looker reports, since a tool on the catalogue that cannot run
// is worse than one that is absent: the pass spends a turn on it and gets an
// error back.
var LookCalls = []string{CallGrep, CallRead, CallDocs, CallSymbol, CallHistory}

// isLookCall reports whether a call is answered from the tree rather than
// recorded as part of the review.
func isLookCall(name string) bool {
	return slices.Contains(LookCalls, name)
}

// MaxReadLines is the cap on one read_lines call, so a pass that asks for a
// file gets a declaration instead of the whole thing.
//
// Exported because the number is in the tool's description, which is a promise
// to the model, and the Looker that enforces it lives in another package. A
// test there pins the two together: a cap that moved without the description
// would leave the model told one thing and given another.
const MaxReadLines = 200

// callTool is one tool's definition as it goes on the wire.
type callTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"input_schema"`
}

// callTools is every tool, in a fixed order. The order is part of the bytes a
// cache read depends on, so it is written out rather than built from a map.
//
// get_context is only there when the run has context to read through it. Left
// in a review with the context inline, a findings pass called it, was told
// nothing was held back, and ended without a comment. Whether it is there is
// fixed for the whole run, so every call of a run still sends the same bytes.
func callTools(pulls bool, looks []string) []callTool {
	all := []callTool{
		{CallOverview, "Set the overview: one or two paragraphs on what this change does and why it exists, " +
			"written for a reviewer about to read the diff. Say what the change is for, not whether it is correct. " +
			"Calling it again replaces the earlier overview.",
			flatObject(map[string]any{"overview": overviewSchema()}, "overview")},
		{CallFile, "Record one line on what one file's change does and why, using the path exactly as it appears in the change. " +
			"Call it once for each file the pass asks you to describe; a second call for the same path replaces the first. " +
			"Describe the change, not its quality.",
			flatObject(propsOf(fileSchema(), "path", "summary"), "path", "summary")},
		{CallCohort, "Record one group of changed files that a reviewer should hold in mind together, such as a schema " +
			"change and the code that reads it. Only for a pass that asks for cohorts. Every file belongs to exactly one " +
			"group, and the summary is what reviewers of the other groups see of this one, so write it for someone who " +
			"cannot see these files.",
			flatObject(propsOf(cohortSchema(), "name", "summary", "files"), "name", "summary", "files")},
		{CallComment, "Record one defect in the change as a review comment. Call it once per defect, including defects " +
			"you are unsure about or consider low severity; the confidence and severity fields carry that. Anchor it to " +
			"the line where the defect is, in the file as it is after the change, and use the question fields to name the " +
			"one check that would confirm or refute it.",
			commentCallSchema()},
		{CallRule, "Record your ruling on one finding you were given, by its id. Call it once for every finding. " +
			"Write the analysis first, then the verdict it leads to, and quote the line the verdict rests on.",
			flatObject(propsOf(rulingItemSchema(), "finding", "analysis", "verdict", "evidence", "why"),
				"finding", "analysis", "verdict", "evidence", "why")},
		{CallContext, "Return the full text of context entries by id, from the lists under each file in the diff. " +
			"A caller entry is code elsewhere that calls something this change touched: when a change alters what a " +
			"function accepts, returns or does, its callers are where that breaks. A type entry is the definition of a " +
			"type the change uses, for checking what a value can hold. A history entry is the commit history of lines " +
			"the change touches or removes, which says why they were there. A sibling entry is another implementation " +
			"of an interface the change affects, and a test entry is a test covering a changed symbol.",
			flatObject(map[string]any{"ids": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "Ids from the index, for example [\"ctx3\", \"ctx7\"].",
			}}, "ids")},
		{CallGrep, "Search the repository for a regular expression, to check a claim before you file it. " +
			"Returns matching lines with their file and line number. It searches this repository and nothing else, " +
			"so a dependency's own source will not be found. It matches text rather than types, so a hit may be a " +
			"different thing with the same name. Use it to find whether the repository already does something " +
			"elsewhere, which is the check a reader performs in seconds and a reviewer that cannot search has to " +
			"guess at.",
			flatObject(map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go regular expression, matched against one line at a time"},
				"glob":    map[string]any{"type": "string", "description": "optional filter: only paths containing this text are searched, for example .go or internal/store/"},
			}, "pattern")},
		{CallRead, fmt.Sprintf("Read a range of lines from a file in the repository, with line numbers. Use it to look at "+
			"code the diff does not show. At most %d lines come back per call, so ask for the declaration you want "+
			"rather than the file.", MaxReadLines),
			flatObject(map[string]any{
				"path":       map[string]any{"type": "string", "description": "repository-relative path"},
				"start_line": map[string]any{"type": "integer", "description": "first line, 1-based"},
				"end_line":   map[string]any{"type": "integer", "description": "last line, inclusive"},
			}, "path", "start_line", "end_line")},
		{CallDocs, "List the repository's own documents with their first heading: design notes, decision records, plans, READMEs. " +
			"Use it when a change looks like it is implementing something that was written down, or when what looks like a defect " +
			"may be a decision the team recorded. Read what looks relevant with read_lines. A rule the repository committed to " +
			"outranks your reading of the diff, so this is the check that keeps a finding off a deliberate choice.",
			map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
		{CallSymbol, "Ask gorefactor for the exact Go context of one symbol: its definition, its callers resolved through the " +
			"type checker, the types in its signature, and its tests. Takes a symbol like Store.Insert or Insert. This resolves " +
			"by type identity rather than by name, so the callers it returns are facts and not leads. It is the call to make when " +
			"a change alters what a function accepts, returns or does, and you need to know who breaks.",
			flatObject(map[string]any{
				"symbol": map[string]any{"type": "string", "description": "Symbol or Receiver.Method, for example Store.Insert"},
			}, "symbol")},
		{CallHistory, "Ask git why a span of lines is there: the commits that last touched them, with their messages and their " +
			"diffs. It is the call to make about code the change removes or rewrites, where the question is what the lines were " +
			"for and whether the reason still holds. Give the line numbers in the file as it is after the change.",
			flatObject(map[string]any{
				"path":       map[string]any{"type": "string", "description": "repository-relative path"},
				"start_line": map[string]any{"type": "integer", "description": "first line, 1-based"},
				"end_line":   map[string]any{"type": "integer", "description": "last line, inclusive"},
			}, "path", "start_line", "end_line")},
		{CallDone, "End this pass once every call it needs has been made. Anything recorded before it stays recorded.",
			map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}}},
	}
	drop := map[string]bool{}
	if !pulls {
		drop[CallContext] = true
	}
	for _, name := range LookCalls {
		if !slices.Contains(looks, name) {
			drop[name] = true
		}
	}
	out := all[:0]
	for _, t := range all {
		if !drop[t.Name] {
			out = append(out, t)
		}
	}
	return out
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
//
// get_context is taken by every pass of a run that has context to read: it
// records nothing, and any pass may want to read what was held back.
//
// The lookups are narrower. They go to the passes that judge, because
// checking a claim is what they are for: a describing pass has nothing to
// check, and the ruling already has a lookup pass of its own whose answers it
// was assembled around. The catalogue still carries them on every call of the
// run, since a tool list that changed between calls would throw away the
// prefix they share; this is what a pass is permitted to call.
func callsFor(stage string, pulls bool, looks []string) []string {
	var calls []string
	switch stage {
	case StageSynopsis:
		calls = []string{CallOverview, CallFile, CallCohort}
	case StageFindings:
		calls = []string{CallComment}
	case StageRuling:
		calls = []string{CallRule}
	default:
		calls = []string{CallOverview, CallFile, CallComment}
	}
	if pulls {
		calls = append(calls, CallContext)
	}
	if stage == StageFindings || stage == StageReview {
		for _, name := range LookCalls {
			if slices.Contains(looks, name) {
				calls = append(calls, name)
			}
		}
	}
	return calls
}

// callsBlock tells a pass how its answer is taken and which calls it takes. It
// says what the tools do and nothing about when or in what order to call them:
// a model that is thinking decides that itself, and two instructions that did
// decide it, one asking for every call in one reply and one asking for one
// area at a time, each moved the reasoning around less than they moved the
// calls. It goes after the cached prompt, so every pass reads the same entry.
func callsBlock(stage string, deferred bool, looks []string) string {
	var takes string
	switch stage {
	case StageSynopsis:
		takes = "set_overview once, describe_file once per file, and add_cohort once per group when the pass asks for groups"
	case StageFindings:
		takes = "add_comment, one call per comment"
	case StageRuling:
		takes = "rule, one call per finding"
	default:
		takes = "set_overview once, describe_file once per file, and add_comment once per comment"
	}
	return "\n\n## How to answer\n\nAnswer by calling the tools. Every call is answered: \"Recorded.\" when it is " +
		"taken, or what is wrong with it when it is not. Call done when this pass is finished.\n\n" +
		"This pass takes " + takes + "." + readsContext(stage, deferred) + checksTree(stage, looks) + "\n"
}

// checksTree is the calls block's line about searching, on the passes that may.
//
// It says what the tools are for rather than when to reach for them, the same
// rule the rest of this block follows. What it does say is the one thing the
// pass cannot work out for itself: a claim about the rest of the repository is
// checkable now, where before it could only be named as a question for a later
// pass.
func checksTree(stage string, looks []string) string {
	if len(looks) == 0 || (stage != StageFindings && stage != StageReview) {
		return ""
	}
	var parts []string
	for _, name := range looks {
		switch name {
		case CallGrep:
			parts = append(parts, "grep searches this repository")
		case CallRead:
			parts = append(parts, "read_lines reads a span of one file")
		case CallDocs:
			parts = append(parts, "list_docs names what the team wrote down")
		case CallSymbol:
			parts = append(parts, "symbol_context resolves one Go symbol's callers through the type checker")
		case CallHistory:
			parts = append(parts, "line_history says why a span of lines is there")
		}
	}
	return " " + joinWithAnd(parts) + "; they answer with what the tree says and record nothing. " +
		"A claim about code outside the diff is one you can check here rather than only name."
}

// joinWithAnd writes a list the way a sentence does, so the calls line reads
// as prose rather than as a comma-separated catalogue.
func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
}

// readsContext is the calls block's line about held-back context, when there
// is any to read.
func readsContext(stage string, deferred bool) string {
	if !deferred {
		return ""
	}
	line := " The context listed under each file in the diff (callers of what changed, the types it uses, " +
		"tests and line history) is one get_context call away, by the ids in those lists."
	if stage == StageFindings || stage == StageReview {
		// The one directive here, and a measured one: across six runs at every
		// effort level a judging pass reasoned through the whole change in its
		// first reply and never read any of the context, and a light
		// instruction is the documented lever for a tool a model under-uses.
		line += " Read the context you need with get_context before writing comments; comments can come in a later reply."
	}
	return line
}

// passExpect is what makes a pass visibly complete. The describing pass is
// complete once it has the overview, a line for every file on its roster and,
// when it was asked for them, cohorts; the ruling once every finding it was
// given has a ruling. A findings pass has no such test, since nothing says how
// many comments a change deserves, and it ends only on done.
type passExpect struct {
	files    []string
	cohorts  bool
	findings []string
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
	expect   passExpect
	// deferred is the held-back context get_context reads from, and fetched
	// how many entries the pass has asked for.
	deferred []deferredEntry
	fetched  int
	// look answers the lookups, and is nil on a run that has none.
	look Looker
	// looked counts the lookups a pass made, for the progress line
	// and the trace: a judging pass that checked nothing is worth telling
	// apart from one that had nothing to check.
	looked int
	// lookCalls is which lookups this run's Looker can answer, resolved once
	// so the catalogue a call is checked against is the catalogue that went
	// out with it.
	lookCalls []string
}

func newCollector(stage string, expect passExpect, deferred []deferredEntry, look Looker) *collector {
	looks := lookCallsFor(look)
	schemas := map[string]map[string]any{}
	for _, t := range callTools(len(deferred) > 0, looks) {
		schemas[t.Name] = t.Schema
	}
	return &collector{stage: stage, schemas: schemas, expect: expect,
		deferred: deferred, look: look, lookCalls: looks}
}

// complete reports whether the pass has recorded everything its expectation
// names, so the loop can end it on the reply that finished the work rather
// than spend another request, which resends the whole conversation, waiting
// for done.
func (c *collector) complete() bool {
	switch c.stage {
	case StageSynopsis:
		if c.overview == "" || (c.expect.cohorts && len(c.cohorts) == 0) {
			return false
		}
		described := map[string]bool{}
		for _, f := range c.files {
			if p, ok := f["path"].(string); ok {
				described[p] = true
			}
		}
		for _, p := range c.expect.files {
			if !described[p] {
				return false
			}
		}
		return true
	case StageRuling:
		if len(c.expect.findings) == 0 {
			return false
		}
		ruled := map[string]bool{}
		for _, r := range c.rulings {
			if id, ok := r["finding"].(string); ok {
				ruled[id] = true
			}
		}
		for _, id := range c.expect.findings {
			if !ruled[id] {
				return false
			}
		}
		return true
	}
	return false
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
		if call.Name != CallDone && !contains(callsFor(c.stage, len(c.deferred) > 0, c.lookCalls), call.Name) {
			// Not a malformed call but one this pass has no use for, so it is
			// not to be sent again.
			rejected++
			results = append(results, callResult{id: call.ID, isError: true,
				content: fmt.Sprintf("Not recorded: this pass does not take %s; it takes %s. Do not send it again.",
					call.Name, strings.Join(callsFor(c.stage, len(c.deferred) > 0, c.lookCalls), ", "))})
			continue
		}
		problems := c.check(call)
		if len(problems) > 0 {
			rejected++
			results = append(results, callResult{id: call.ID, isError: true,
				content: "Not recorded: " + strings.Join(problems, "; ") + ". Send this call again with that fixed."})
			continue
		}
		if call.Name == CallContext {
			// Answered with the context itself. Nothing is recorded, so it
			// neither completes a pass nor counts toward one.
			var asked struct {
				IDs []string `json:"ids"`
			}
			_ = json.Unmarshal(call.Input, &asked)
			c.fetched += len(asked.IDs)
			results = append(results, callResult{id: call.ID, content: fetchDeferred(c.deferred, call.Input)})
			continue
		}
		if isLookCall(call.Name) {
			// Answered with what the tree says. Nothing is recorded, so it
			// neither completes a pass nor counts toward one, the same as
			// get_context.
			c.looked++
			results = append(results, callResult{id: call.ID, content: c.lookup(call)})
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

// lookup answers a call the tree can settle rather than one that records part
// of the review.
//
// An error comes back as the answer rather than as a rejection: a bad pattern
// or a path that does not exist is something the pass can act on, and sending
// it back as "not recorded" would read as a call this pass may not make.
func (c *collector) lookup(call toolCall) string {
	if c.look == nil {
		return "no lookup is available on this run"
	}
	switch call.Name {
	case CallGrep:
		var in struct {
			Pattern string `json:"pattern"`
			Glob    string `json:"glob"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return "bad arguments: " + err.Error()
		}
		out, err := c.look.Grep(in.Pattern, in.Glob)
		if err != nil {
			return err.Error()
		}
		if strings.TrimSpace(out) == "" {
			// Said, so a pass can tell "nothing matches" from "the search
			// failed". The first is an answer and the second is not.
			return "no match in this repository"
		}
		return out
	case CallRead:
		var in struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return "bad arguments: " + err.Error()
		}
		out, err := c.look.ReadLines(in.Path, in.StartLine, in.EndLine)
		if err != nil {
			return err.Error()
		}
		return out
	case CallDocs:
		out, err := c.look.ListDocs()
		if err != nil {
			return err.Error()
		}
		if strings.TrimSpace(out) == "" {
			return "no documents in this repository"
		}
		return out
	case CallSymbol:
		var in struct {
			Symbol string `json:"symbol"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return "bad arguments: " + err.Error()
		}
		out, err := c.look.SymbolContext(in.Symbol)
		if err != nil {
			return err.Error()
		}
		if strings.TrimSpace(out) == "" {
			// Said, for grep's reason: a symbol the type checker does not
			// know is an answer, and a pass told nothing cannot tell that
			// from a lookup that broke.
			return "gorefactor knows no symbol by that name"
		}
		return out
	case CallHistory:
		var in struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return "bad arguments: " + err.Error()
		}
		out, err := c.look.LineHistory(in.Path, in.StartLine, in.EndLine)
		if err != nil {
			return err.Error()
		}
		return out
	}
	return "no tool named " + call.Name
}
