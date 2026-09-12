package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/chrisophus/redline/internal/envelope"
)

// toolTimeout bounds one tool call. A scout tool shells out to git, to
// gorefactor or to graphify, and a hung subprocess inside a provider is a
// hung review: Redline's own provider timeout would fire, but only after
// five minutes of a review that had already been paid for.
const toolTimeout = 45 * time.Second

// tool is one thing the scout can do. Run returns the text handed back to the
// model; an error is handed back as an error result rather than ending the
// run, because a scout that mistypes a symbol should correct itself rather
// than take the review down with it.
type tool struct {
	name        string
	description string
	schema      anthropic.ToolInputSchemaParam
	run         func(input json.RawMessage) (string, error)
}

// toolset is what this repository's checkout can actually offer. gorefactor
// and graphify are registered only when they are there to run: a tool the
// model can see and cannot use costs a turn to discover.
type toolset struct {
	tools   []tool
	byName  map[string]tool
	records []record
	notes   []string
	done    bool
	// closing narrows the offered tools to record and done. Set for the last
	// turn of the budget: a search cut off mid-lookup files nothing, and
	// everything it read is paid for and then thrown away.
	closing bool
	// answering is set when the run has questions. A record then wants an
	// id to be filed against, and one without gets told so.
	answering bool
	// calls and refused count the tool calls of the turn being dispatched.
	// The drivers reset them before each turn and read them after, to tell a
	// turn that did something from one that spent itself being corrected.
	calls, refused int

	res    *resolver
	root   string
	graph  string
	limits Limits
	// debug, when set, is called with each tool call and what it returned.
	debug func(string)
}

func newToolset(root, graph string, limits Limits) *toolset {
	ts := &toolset{
		byName: map[string]tool{},
		res:    newResolver(root, limits),
		root:   root,
		graph:  graph,
		limits: limits.withDefaults(),
	}
	ts.register(ts.readLines())
	ts.register(ts.grep())
	ts.register(ts.listDocs())
	if _, err := exec.LookPath("gorefactor"); err == nil {
		ts.register(ts.gorefactorContext())
	}
	if graph != "" && exists(graph) {
		if _, err := exec.LookPath("graphify"); err == nil {
			ts.register(ts.graphAffected())
			ts.register(ts.graphPath())
		}
	}
	ts.register(ts.record())
	ts.register(ts.finish())
	return ts
}

func (ts *toolset) register(t tool) {
	ts.tools = append(ts.tools, t)
	ts.byName[t.name] = t
}

// offered is the toolset the next request carries. On the closing turn it is
// record and done alone: the lookups are over, and the only thing left that
// can help the review is filing what was found.
func (ts *toolset) offered() []tool {
	if !ts.closing {
		return ts.tools
	}
	out := make([]tool, 0, 2)
	for _, t := range ts.tools {
		if t.name == recordTool || t.name == doneTool {
			out = append(out, t)
		}
	}
	return out
}

// params renders the toolset for the request.
func (ts *toolset) params() []anthropic.ToolUnionParam {
	offered := ts.offered()
	out := make([]anthropic.ToolUnionParam, 0, len(offered))
	for _, t := range offered {
		def := anthropic.ToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: t.schema,
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &def})
	}
	return out
}

// dispatch runs one tool call and returns the result text and whether it
// failed.
func (ts *toolset) dispatch(name string, input json.RawMessage) (out string, failed bool) {
	ts.calls++
	defer func() {
		if failed {
			ts.refused++
		}
		if ts.debug != nil {
			ts.debug(fmt.Sprintf("tool %s(%s) → %s", name, toolArgs(input), toolResult(out, failed)))
		}
	}()
	t, ok := ts.byName[name]
	if !ok {
		return fmt.Sprintf("no tool named %q", name), true
	}
	out, err := t.run(input)
	if err != nil {
		return err.Error(), true
	}
	if strings.TrimSpace(out) == "" {
		return "(no output)", false
	}
	return out, false
}

// toolArgs renders a tool call's arguments on one bounded line for a debug log.
func toolArgs(input json.RawMessage) string {
	s := strings.Join(strings.Fields(string(input)), " ")
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// toolResult says what a tool handed back: the byte count on success, or the
// bounded error text, which is what the model saw and corrected from.
func toolResult(out string, failed bool) string {
	if failed {
		s := strings.Join(strings.Fields(out), " ")
		if len(s) > 100 {
			s = s[:100] + "…"
		}
		return "error: " + s
	}
	return fmt.Sprintf("%d bytes", len(out))
}

// startTurn opens a turn's accounting. Called by both drivers before the tool
// calls of that turn are dispatched.
func (ts *toolset) startTurn() { ts.calls, ts.refused = 0, 0 }

// refusedEveryCall reports that the turn just dispatched did nothing but get
// corrected. A refusal is a correction the scout is meant to act on, and on
// the closing turn there is no turn left to act in: on PR #38 two records were
// refused on the last turn, the transcript ended there, and a finding came
// back unverifiable on evidence that had already been found. The drivers buy
// one more filing turn when this is true, once.
func (ts *toolset) refusedEveryCall() bool { return ts.calls > 0 && ts.refused == ts.calls }

// Names is what the prompt tells the model it has, so it reads from the tools
// actually offered: on the closing turn a prompt still ending "Your tools:
// grep, read_lines, …" invites lookups the request no longer carries.
func (ts *toolset) Names() []string {
	offered := ts.offered()
	out := make([]string, 0, len(offered))
	for _, t := range offered {
		out = append(out, t.name)
	}
	sort.Strings(out)
	return out
}

func schema(props map[string]any, required ...string) anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{Properties: props, Required: required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

// The two caps read_lines and grep enforce. Named because each is stated in a
// tool description and applied at a call site, and a limit a model is told
// about in one place and held to in another is the drift this file already
// fixed once for record.
const (
	maxReadLines   = 200
	maxGrepMatches = 60
)

func (ts *toolset) readLines() tool {
	return tool{
		name: "read_lines",
		description: fmt.Sprintf("Read a range of lines from a file in the repository, with line numbers. "+
			"Use it to look at code the diff does not show. At most %d lines come back per call: a "+
			"wider range is cut to the first %d from start_line, so ask for the declaration you want "+
			"rather than the file. A path that does not exist, or a start_line past the end of the "+
			"file, is an error rather than an empty result.",
			maxReadLines, maxReadLines),
		schema: schema(map[string]any{
			"path":       str("repository-relative path"),
			"start_line": num("first line, 1-based"),
			"end_line":   num("last line, inclusive"),
		}, "path", "start_line", "end_line"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Path      string `json:"path"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			lines, err := ts.res.read(in.Path)
			if err != nil {
				return "", fmt.Errorf("%s: %v", in.Path, err)
			}
			start, end := clamp(in.StartLine, in.EndLine, len(lines), maxReadLines)
			if start == 0 {
				return "", fmt.Errorf("%s has %d lines; %d is past the end", in.Path, len(lines), in.StartLine)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s:%d-%d\n", normPath(in.Path), start, end)
			for i := start; i <= end; i++ {
				fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
			}
			return b.String(), nil
		},
	}
}

func (ts *toolset) grep() tool {
	return tool{
		name: "grep",
		description: fmt.Sprintf("Search the repository for a regular expression. Returns matching lines with "+
			"their file and line number, at most %d of them, walking the whole tree except .git, "+
			"vendor, node_modules and build output. It searches this repository and nothing else: a "+
			"dependency's own source is not in the tree, so no pattern will find it there. Use it to "+
			"find who mentions a changed symbol. It matches text rather than types, so a hit may be a "+
			"different thing with the same name; where gorefactor_context can answer instead, it "+
			"resolves through the type checker and its callers are facts. Files over 1 MB are "+
			"skipped, and it says so when it stops at the cap.",
			maxGrepMatches),
		schema: schema(map[string]any{
			"pattern": str("Go regular expression, matched against one line at a time"),
			"glob":    str("optional filter: only paths containing this text are searched, for example .go or internal/store/"),
		}, "pattern"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Pattern string `json:"pattern"`
				Glob    string `json:"glob"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			re, err := regexp.Compile(in.Pattern)
			if err != nil {
				return "", fmt.Errorf("bad pattern: %v", err)
			}
			return grepTree(ts.root, re, in.Glob, maxGrepMatches)
		},
	}
}

func (ts *toolset) listDocs() tool {
	return tool{
		name: "list_docs",
		description: "List the repository's documents with their first heading: design notes, decision records, plans, READMEs. " +
			"Use it when a change looks like it is implementing something that was written down, or when its intent is not obvious from the diff. Read what looks relevant with read_lines.",
		schema: schema(map[string]any{}),
		run: func(json.RawMessage) (string, error) {
			const max = 80
			docs := listDocs(ts.root, max+1)
			if len(docs) == 0 {
				return "no documents in this repository", nil
			}
			cut := len(docs) > max
			if cut {
				docs = docs[:max]
			}
			var b strings.Builder
			for _, d := range docs {
				fmt.Fprintf(&b, "%s (%d lines)", d.Path, d.Lines)
				if d.Heading != "" {
					fmt.Fprintf(&b, ": %s", d.Heading)
				}
				b.WriteString("\n")
			}
			if cut {
				// Said, so a design note that sorts after the cut is a gap
				// the scout knows about rather than one it cannot.
				fmt.Fprintf(&b, "(stopped at %d documents, sorted by path; there are more, so grep for a word a document would use)\n", max)
			}
			return b.String(), nil
		},
	}
}

func (ts *toolset) gorefactorContext() tool {
	return tool{
		name:        "gorefactor_context",
		description: "Ask gorefactor for the exact Go context of one symbol: its definition, its callers resolved through the type checker, the types in its signature, and its tests. Takes a symbol like Store.Insert or Insert. This resolves by type identity, not by name, so its callers are facts.",
		schema:      schema(map[string]any{"symbol": str("Symbol or Receiver.Method")}, "symbol"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Symbol string `json:"symbol"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			return runCmd(ts.root, "gorefactor", "context", "--json", "--", in.Symbol)
		},
	}
}

func (ts *toolset) graphAffected() tool {
	return tool{
		name:        "graph_affected",
		description: "Ask the repository graph what would be affected by a node, across every language it indexes including SQL and config. Matches by name, not by type, so treat the answer as a lead rather than a fact.",
		schema: schema(map[string]any{
			"label": str("node label, for example Insert() or a table name"),
			"depth": num("traversal depth, 1 or 2 (default 1)"),
		}, "label"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Label string `json:"label"`
				Depth int    `json:"depth"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			if in.Depth < 1 || in.Depth > 2 {
				in.Depth = 1
			}
			return runCmd(ts.root, "graphify", "affected",
				"--depth", fmt.Sprint(in.Depth), "--graph", ts.graph, "--", in.Label)
		},
	}
}

func (ts *toolset) graphPath() tool {
	return tool{
		name:        "graph_path",
		description: "Ask the repository graph how two things connect, across languages. This is the question to ask when a change touches code and a migration, a config file or a schema: give it the code symbol and the other thing's name.",
		schema: schema(map[string]any{
			"from": str("node label to start at"),
			"to":   str("node label to reach"),
		}, "from", "to"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			return runCmd(ts.root, "graphify", "path", "--graph", ts.graph, "--", in.From, in.To)
		},
	}
}

// The two tools that end a search rather than extend it: everything the
// review sees arrives through record, and done says what could not be found.
// Named because the closing turn offers exactly these.
const (
	recordTool = "record"
	doneTool   = "done"
)

func (ts *toolset) record() tool {
	return tool{
		name: recordTool,
		description: fmt.Sprintf("Put a range of code in front of the reviewer. You choose the range and the role; this program reads the bytes from the tree itself, so record the location rather than the code. "+
			"A range longer than %d lines is cut to the first %d from start_line, so record the declaration rather than the file around it. "+
			"Three calls are refused, and the error comes back to you to correct: a role the brief says another provider already covers, a test file, and any call past %d records, which the count on each successful call tracks for you. "+
			"Roles: enclosing (the whole declaration a changed hunk sits in), caller (a call site of something the change touched), type (a type named in a changed signature), sibling (another implementation of an interface the change touches), history (what git says about those lines, for a deleted guard or a reverted fix), neighbor (a file of another kind the change is coupled to, such as a migration or a config file), guideline (a rule this repository wrote down that bears on this change: a house style, a convention, the paragraph of a design doc that says why something is the way it is).",
			ts.limits.MaxLines, ts.limits.MaxLines, ts.limits.MaxRecords),
		schema: schema(map[string]any{
			"role":       str("enclosing, caller, type, sibling, history, neighbor or guideline"),
			"file":       str("repository-relative path"),
			"start_line": num("first line, 1-based"),
			"end_line":   num("last line, inclusive"),
			"symbol":     str("what this is, for the header the reviewer sees"),
			"found_via":  str("how you found it: diff, grep, gorefactor, graph, read, history or docs"),
			"answers":    str("when you were given questions, the id in brackets of the one this answers"),
		}, "role", "file", "start_line", "end_line", "symbol"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Role      string `json:"role"`
				File      string `json:"file"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				Symbol    string `json:"symbol"`
				FoundVia  string `json:"found_via"`
				Answers   string `json:"answers"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			rec := record{
				Role:      envelopeRole(in.Role),
				File:      normPath(in.File),
				StartLine: in.StartLine,
				EndLine:   in.EndLine,
				Symbol:    strings.TrimSpace(in.Symbol),
				FoundVia:  strings.ToLower(strings.TrimSpace(in.FoundVia)),
				Answers:   strings.Trim(strings.TrimSpace(in.Answers), "[]"),
			}
			if err := ts.res.validate(rec); err != nil {
				return "", err
			}
			if rec.Answers != "" {
				// A tagged record replaces an untagged twin. The reminder
				// below asks for exactly this second call, and the first
				// record would otherwise reach the ruling as a duplicate
				// tied to nothing.
				ts.records = slices.DeleteFunc(ts.records, func(r record) bool {
					return r.Answers == "" && r.Role == rec.Role && r.File == rec.File &&
						r.StartLine == rec.StartLine && r.EndLine == rec.EndLine
				})
			}
			// Checked after the dedup above rather than before it. A tagged
			// record that replaces an untagged twin leaves the count exactly
			// where it was, and the reminder below asks the model for that
			// very call; refusing it at the limit made the tool contradict
			// its own instruction on every run that filled its budget.
			if len(ts.records) >= ts.limits.MaxRecords {
				return "", fmt.Errorf("already recorded %d ranges, which is the limit; call done", ts.limits.MaxRecords)
			}
			ts.records = append(ts.records, rec)
			out := fmt.Sprintf("recorded %s %s:%d-%d (%d of %d)",
				rec.Role, rec.File, rec.StartLine, rec.EndLine, len(ts.records), ts.limits.MaxRecords)
			if ts.answering && rec.Answers == "" {
				// Accepted, because the evidence is real either way, and
				// told: an untagged answer reaches the ruling as something
				// found but tied to nothing, which one more call can fix.
				out += "; it is tied to no question, so record it again with answers set to the id in brackets of the finding it bears on"
			}
			return out, nil
		},
	}
}

func (ts *toolset) finish() tool {
	return tool{
		name:        doneTool,
		description: "Finish. Call this when you have recorded what the reviewer needs. Give notes for anything you looked for and could not establish: those reach the report as unknowns, and a gap nobody names reads exactly like a gap that is not there.",
		schema: schema(map[string]any{
			"notes": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "what you could not determine, one short sentence each, naming what you looked for; when you were given questions, start each with the id in brackets of the one it is about",
			},
		}),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Notes []string `json:"notes"`
			}
			// Bad arguments here must not strand the loop: done is how it
			// ends, so accept the call and drop the notes.
			_ = json.Unmarshal(input, &in)
			for _, n := range in.Notes {
				if n = strings.TrimSpace(n); n != "" {
					ts.notes = append(ts.notes, n)
				}
			}
			ts.done = true
			return "done", nil
		},
	}
}

// questionKindRoles maps the two question kinds that name no record role onto
// the role that carries the same evidence.
//
// The brief teaches five kinds of question - precedent, caller, rule, history,
// type - and record accepts a different seven roles. Three of the words are in
// both lists, so tagging a record with the kind it answers works for those and
// is refused for precedent and rule. On PR #38 the scout answered a precedent
// question, said so out loud, filed two records under role "precedent", was
// refused twice, and ran out of turns: the finding came back unverifiable on
// evidence that had already been found. A vocabulary a model is invited to
// confuse should be accepted rather than corrected on the last turn.
var questionKindRoles = map[envelope.Role]envelope.Role{
	// A precedent is somewhere else in the tree doing the same thing, which
	// is what neighbor already means.
	"precedent": RoleNeighbor,
	// A rule the team wrote down is a guideline.
	"rule": RoleGuideline,
}

func envelopeRole(s string) envelope.Role {
	role := envelope.Role(strings.ToLower(strings.TrimSpace(s)))
	if mapped, ok := questionKindRoles[role]; ok {
		return mapped
	}
	return role
}

func runCmd(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), toolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// Stdout and Stderr here are not *os.File, so os/exec copies them through
	// a pipe and Run blocks until every write end is closed. Killing the tool
	// on the deadline does not close a pipe a grandchild still holds, and
	// these tools spawn exactly those: graphify runs Python, gorefactor runs
	// go list. WaitDelay is what makes the deadline enforceable.
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s did not finish within %s", name, toolTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", name, firstLine(msg))
	}
	return out.String(), nil
}

// grepTree walks the repository for a pattern. Written here rather than
// shelled out to ripgrep so the tool exists on every machine, and bounded on
// both file size and match count so one broad pattern cannot fill the
// scout's context with its own search results.
func grepTree(root string, re *regexp.Regexp, glob string, max int) (string, error) {
	// The walk collects paths and reads nothing. Reading inside the callback
	// means acting on a path the walk resolved earlier, which is a symlink
	// race the moment the tree is not yours alone; it is also what gosec's
	// G122 is about. Two passes cost one slice and remove the question.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	var candidates []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "graphify-out", ".redline":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if glob != "" && !strings.Contains(rel, glob) {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
			return nil
		}
		// A symlink can point a walked path at a file outside the tree.
		// Resolve it and skip anything whose real path escapes the root,
		// so its bytes never reach the envelope.
		if d.Type()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil
			}
			out, err := filepath.Rel(realRoot, resolved)
			if err != nil || out == ".." || strings.HasPrefix(out, ".."+string(filepath.Separator)) {
				return nil
			}
		}
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(candidates)

	var b strings.Builder
	matches := 0
	for _, rel := range candidates {
		if matches >= max {
			break
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if matches >= max {
				break
			}
			if re.MatchString(line) {
				matches++
				fmt.Fprintf(&b, "%s:%d: %s\n", rel, i+1, strings.TrimSpace(line))
			}
		}
	}
	if matches == 0 {
		// An empty search is an answer, and it has to say what it searched.
		// A question about a dependency's behaviour lands here, and a bare
		// "no matches" reads as "this is nowhere in the code" when what
		// happened is that the code is outside the tree: the module cache and
		// everything else off the repository is not walked.
		return "no matches in this repository, which is the whole of what this searches; " +
			"code in a dependency is outside it and cannot be found from here", nil
	}
	if matches >= max {
		fmt.Fprintf(&b, "(stopped at %d matches; narrow the pattern or the glob)\n", max)
	}
	return b.String(), nil
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
