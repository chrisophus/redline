package review

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
)

// Context held back until asked for.
//
// By default the context the providers resolved goes into the prompt whole,
// ahead of the diff. With DeferContext it does not. Each file's diff is
// followed by a short index of the context that belongs to it, one line per
// entry naming its role, symbol and location, and a pass reads an entry by
// calling get_context with its id. The same entries are offered either way:
// the ones that fit the context room, after the redundant ones and the held
// back test code are dropped.
//
// It is an experiment in whether a reviewer that pulls context as its
// reasoning calls for it does better than one handed everything up front.
// The model decides what to read and when, including in the middle of a pass,
// and what it read rides in the conversation from then on.

// CallContext is the tool that returns held-back context.
const CallContext = "get_context"

// contextIDPrefix marks a context entry's id, kept apart from the ids the
// ruling gives findings.
const contextIDPrefix = "ctx"

// deferredEntry is one piece of held-back context and the changed file it
// belongs to, empty when it belongs to none.
type deferredEntry struct {
	x      envelope.Expansion
	anchor string
}

// deferEntries assigns every kept expansion to the changed file it is about.
//
// A provider's scope names the changed symbol an expansion was resolved for,
// and its enclosing expansion for that symbol carries the file the symbol is
// in, so the scope leads to the file even when the expansion itself - a call
// site, a type, a test - lives somewhere else. Scopes are matched as whole
// strings and never parsed, which is all the envelope contract allows. An
// expansion with no scope that leads to a shown file is placed by its own
// file, and one that does neither is listed for the change as a whole.
func deferEntries(in Input, kept []envelope.Expansion) []deferredEntry {
	shown := in.ShownFiles()
	scopeFile := map[string]string{}
	for _, env := range in.Envelopes {
		if env == nil {
			continue
		}
		for _, x := range env.Expansions {
			if x.Role == envelope.RoleEnclosing && x.Scope != "" && shown[x.File] {
				scopeFile[x.Scope] = x.File
			}
		}
	}
	out := make([]deferredEntry, 0, len(kept))
	for _, x := range kept {
		anchor := ""
		switch {
		case x.Scope != "" && scopeFile[x.Scope] != "":
			anchor = scopeFile[x.Scope]
		case shown[x.File]:
			anchor = x.File
		}
		out = append(out, deferredEntry{x: x, anchor: anchor})
	}
	return out
}

// pulls reports whether this run reads context through get_context, which is
// what decides whether the tool is offered at all.
func (r *Result) pulls() bool {
	return len(r.deferred) > 0
}

// looks is which lookups this run can make against the tree, which is what
// decides which of them are offered at all.
func (r *Result) looks() []string {
	return r.lookCalls
}

// lookCallsFor is the catalogue a Looker can serve, filtered to the calls this
// package defines and put in catalogue order.
//
// Filtering rather than trusting the list keeps one bad answer from a Looker
// from putting a tool on the wire that nothing dispatches, and the order is
// fixed here because it is part of the bytes a cache read depends on.
func lookCallsFor(l Looker) []string {
	if l == nil {
		return nil
	}
	can := l.Calls()
	var out []string
	for _, name := range LookCalls {
		if slices.Contains(can, name) {
			out = append(out, name)
		}
	}
	return out
}

// contextID is an entry's id as the index shows it and get_context takes it.
func contextID(i int) string {
	return fmt.Sprintf("%s%d", contextIDPrefix, i+1)
}

// indexLine is one entry of the index: no content, only enough to decide
// whether to ask for it.
func indexLine(i int, x envelope.Expansion) string {
	parts := []string{string(x.Role)}
	if x.Symbol != "" {
		parts = append(parts, x.Symbol)
	}
	loc := x.File
	switch n := x.EndLine - x.StartLine + 1; {
	case x.StartLine <= 0:
	case n == 1:
		loc = fmt.Sprintf("%s:%d (1 line)", x.File, x.StartLine)
	default:
		loc = fmt.Sprintf("%s:%d-%d (%d lines)", x.File, x.StartLine, x.EndLine, n)
	}
	parts = append(parts, loc)
	return fmt.Sprintf("- `%s` %s\n", contextID(i), strings.Join(parts, " · "))
}

// fileIndex is the index for one changed file, empty when nothing belongs to it.
func fileIndex(entries []deferredEntry, file string) string {
	var b strings.Builder
	for i, e := range entries {
		if e.anchor == file {
			b.WriteString(indexLine(i, e.x))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "More context for this file:\n\n" + b.String() + "\n"
}

// changeIndex introduces the index, once, ahead of the diff, and lists the
// entries that belong to no single changed file. It says what the model can
// do with the entries and nothing about why they are not in the prompt.
func changeIndex(entries []deferredEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## More context\n\nCallers, types, tests and line history are available for this change. " +
		"Each file's diff below lists what there is for it, prefixed with its id. " +
		"Pass the ids to get_context to read those entries in full.\n\n")
	for i, e := range entries {
		if e.anchor == "" {
			b.WriteString(indexLine(i, e.x))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// fetchDeferred answers a get_context call: each entry asked for, in full, and
// a line for each id that names nothing.
func fetchDeferred(entries []deferredEntry, input json.RawMessage) string {
	var in struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(input, &in)
	if len(entries) == 0 {
		return "No context is held back in this review; everything resolved is in the prompt."
	}
	var b strings.Builder
	seen := map[int]bool{}
	for _, id := range in.IDs {
		n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(id), contextIDPrefix))
		if err != nil || n < 1 || n > len(entries) {
			fmt.Fprintf(&b, "%s: no such id\n\n", id)
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		x := entries[n-1].x
		loc := x.File
		if x.StartLine > 0 {
			loc = fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
		}
		fmt.Fprintf(&b, "── %s · %s · %s · %s ──\n", contextID(n-1), x.Role, x.Symbol, loc)
		keys := make([]string, 0, len(x.Details))
		for k := range x.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s: %s\n", k, x.Details[k])
		}
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(x.Content, "\n"))
	}
	if b.Len() == 0 {
		return "No ids were given; pass the ids from the index, for example [\"ctx3\"]."
	}
	return b.String()
}
