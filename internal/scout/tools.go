package scout

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	res    *resolver
	root   string
	graph  string
	limits Limits
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

// params renders the toolset for the request.
func (ts *toolset) params() []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(ts.tools))
	for _, t := range ts.tools {
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
func (ts *toolset) dispatch(name string, input json.RawMessage) (string, bool) {
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

// Names lists the registered tools, for the run's own log line.
func (ts *toolset) Names() []string {
	out := make([]string, 0, len(ts.tools))
	for _, t := range ts.tools {
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

func (ts *toolset) readLines() tool {
	return tool{
		name:        "read_lines",
		description: "Read a range of lines from a file in the repository, with line numbers. Use it to look at code the diff does not show.",
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
			start, end := clamp(in.StartLine, in.EndLine, len(lines), 200)
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
		name:        "grep",
		description: "Search the repository for a regular expression. Returns matching lines with their file and line number, capped. Use it to find who mentions a changed symbol.",
		schema: schema(map[string]any{
			"pattern": str("Go regular expression"),
			"glob":    str("optional path suffix filter, for example .go or internal/store/"),
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
			return grepTree(ts.root, re, in.Glob, 60)
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
			return runCmd(ts.root, "gorefactor", "context", in.Symbol, "--json")
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
			return runCmd(ts.root, "graphify", "affected", in.Label,
				"--depth", fmt.Sprint(in.Depth), "--graph", ts.graph)
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
			return runCmd(ts.root, "graphify", "path", in.From, in.To, "--graph", ts.graph)
		},
	}
}

func (ts *toolset) record() tool {
	return tool{
		name: "record",
		description: "Put a range of code in front of the reviewer. You choose the range and the role; this program reads the bytes from the tree itself, so record the location rather than the code. " +
			"Roles: enclosing (the whole declaration a changed hunk sits in), caller (a call site of something the change touched), type (a type named in a changed signature), sibling (another implementation of an interface the change touches), history (what git says about those lines, for a deleted guard or a reverted fix), neighbor (a file of another kind the change is coupled to, such as a migration or a config file).",
		schema: schema(map[string]any{
			"role":       str("enclosing, caller, type, sibling, history or neighbor"),
			"file":       str("repository-relative path"),
			"start_line": num("first line, 1-based"),
			"end_line":   num("last line, inclusive"),
			"symbol":     str("what this is, for the header the reviewer sees"),
			"found_via":  str("how you found it: diff, grep, gorefactor, graph, read or history"),
		}, "role", "file", "start_line", "end_line", "symbol"),
		run: func(input json.RawMessage) (string, error) {
			var in struct {
				Role      string `json:"role"`
				File      string `json:"file"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
				Symbol    string `json:"symbol"`
				FoundVia  string `json:"found_via"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %v", err)
			}
			if len(ts.records) >= ts.limits.MaxRecords {
				return "", fmt.Errorf("already recorded %d ranges, which is the limit; call done", ts.limits.MaxRecords)
			}
			rec := record{
				Role:      envelopeRole(in.Role),
				File:      normPath(in.File),
				StartLine: in.StartLine,
				EndLine:   in.EndLine,
				Symbol:    strings.TrimSpace(in.Symbol),
				FoundVia:  strings.ToLower(strings.TrimSpace(in.FoundVia)),
			}
			if err := ts.res.validate(rec); err != nil {
				return "", err
			}
			ts.records = append(ts.records, rec)
			return fmt.Sprintf("recorded %s %s:%d-%d (%d of %d)",
				rec.Role, rec.File, rec.StartLine, rec.EndLine, len(ts.records), ts.limits.MaxRecords), nil
		},
	}
}

func (ts *toolset) finish() tool {
	return tool{
		name:        "done",
		description: "Finish. Call this when you have recorded what the reviewer needs. Give notes for anything you looked for and could not establish: those reach the report as unknowns, and a gap nobody names reads exactly like a gap that is not there.",
		schema: schema(map[string]any{
			"notes": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "what you could not determine, one short sentence each",
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

func envelopeRole(s string) envelope.Role {
	return envelope.Role(strings.ToLower(strings.TrimSpace(s)))
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	timer := time.AfterFunc(toolTimeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	err := cmd.Run()
	timer.Stop()
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
	var b strings.Builder
	matches := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || matches >= max {
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
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
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
		return nil
	})
	if err != nil {
		return "", err
	}
	if matches == 0 {
		return "no matches", nil
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
