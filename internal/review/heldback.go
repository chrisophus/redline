package review

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/change"
)

// Diffs held back until asked for.
//
// A change whose diff and findings alone are over the ceiling used to be
// refused. Now the review is built with some of the diffs left out: context is
// deferred behind get_context, every changed file is still named in the
// prompt, as many diffs as fit are shown, and the rest are read one at a time
// with get_diff. The reviewer already pulls what it needs with read_lines,
// grep and the others, and a file's diff was the one thing it could not pull.
//
// Only when the file list and the fixed parts of the prompt are over the
// ceiling with every diff held back does the run still refuse.

// CallDiff is the tool that returns a held-back file's diff.
const CallDiff = "get_diff"

// HeldDiffs says which diffs were held back and which of those the reviewer
// went on to read. It is the accounting the posted review shows.
type HeldDiffs struct {
	// Total is how many changed files had a diff to show.
	Total int `json:"total"`
	// Held is the paths whose diffs were not in the prompt, in change order.
	Held []string `json:"held"`
	// Read is the held paths the reviewer asked get_diff for, in the order
	// it first asked.
	Read []string `json:"read,omitempty"`
}

// Unread is the held paths the reviewer never looked at.
func (h *HeldDiffs) Unread() []string {
	if h == nil {
		return nil
	}
	read := map[string]bool{}
	for _, p := range h.Read {
		read[p] = true
	}
	var out []string
	for _, p := range h.Held {
		if !read[p] {
			out = append(out, p)
		}
	}
	return out
}

// holdBack decides whether this request has to leave diffs out, and if so
// returns the input and options to build it from. It is a no-op for a request
// that fits, for one already held back, and for one that would not fit even
// with every diff held back: that one is refused later, as before.
//
// Files are shown in this order until the next one does not fit: non-test
// files before test files, then files the description or the stated intent
// names, then larger diffs first. A file too big for the room that is left is
// skipped, and a smaller one after it may still fit.
func holdBack(in Input, opts Options) (Input, Options) {
	if in.held != nil || in.Change == nil || opts.heldDiffs {
		return in, opts
	}
	if opts.Mode == ModeExplore || opts.Shape() == PipelineStaged {
		// Explore has its own loop and a split review its own cohorts, and
		// neither reads get_diff. They are refused over the ceiling as before.
		return in, opts
	}
	if in.fixedFor(opts).fixed <= opts.Ceiling {
		return in, opts
	}
	var cand []change.File
	for _, f := range in.Change.Files {
		if f.Diff != "" || f.Head != "" {
			cand = append(cand, f)
		}
	}
	if len(cand) == 0 {
		return in, opts
	}
	held := in
	held.held = make(map[string]bool, len(cand))
	for _, f := range cand {
		held.held[f.Path] = true
	}
	tight := opts
	tight.DeferContext = true
	tight.heldDiffs = true
	if held.fixedFor(tight).fixed > tight.Ceiling {
		return in, opts
	}
	said := in.intentText()
	order := append([]change.File(nil), cand...)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if ta, tb := change.IsTest(a.Path), change.IsTest(b.Path); ta != tb {
			return tb
		}
		if na, nb := names(said, a.Path), names(said, b.Path); na != nb {
			return na
		}
		return a.Added+a.Removed > b.Added+b.Removed
	})
	for _, f := range order {
		delete(held.held, f.Path)
		if held.fixedFor(tight).fixed > tight.Ceiling {
			held.held[f.Path] = true
		}
	}
	if len(held.held) == 0 {
		tight.heldDiffs = false
	}
	return held, tight
}

// names reports whether the description or intent text mentions a file, by its
// path or by its base name.
func names(text, path string) bool {
	return strings.Contains(text, path) || strings.Contains(text, filepath.Base(path))
}

// intentText is the pull request's title and body and the commit messages: what
// the author said the change is, searched for the files it names.
func (in Input) intentText() string {
	var b strings.Builder
	if pr := in.Change.Target; pr != nil && pr.PR != nil {
		b.WriteString(pr.PR.Title + "\n" + pr.PR.Body + "\n")
	}
	for _, c := range in.Change.Commits {
		b.WriteString(c.Subject + "\n" + c.Body + "\n")
	}
	return b.String()
}

// heldFiles is the changed files whose diffs are held back, in change order.
func (in Input) heldFiles() []change.File {
	if in.Change == nil || len(in.held) == 0 {
		return nil
	}
	var out []change.File
	for _, f := range in.Change.Files {
		if in.held[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// heldList is the prompt's list of files whose diffs are not shown: one line
// each with its path, its lines added and removed, and whether it is a test.
func (in Input) heldList() string {
	files := in.heldFiles()
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "These %d file(s) are held back to keep the request inside its size limit, "+
		"and their diffs are not shown below. Call %s with a path to read one.\n\n", len(files), CallDiff)
	for _, f := range files {
		kind := ""
		if change.IsTest(f.Path) {
			kind = ", test"
		}
		fmt.Fprintf(&b, "- %s (+%d -%d%s)\n", f.Path, f.Added, f.Removed, kind)
	}
	b.WriteString("\n")
	return b.String()
}

// heldDiffs is what get_diff answers from: each held file's diff, rendered as
// the prompt would have rendered it.
func (in Input) heldDiffs() map[string]string {
	files := in.heldFiles()
	if len(files) == 0 {
		return nil
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Path] = fileSection(f, nil)
	}
	return out
}

// heldSummary is the accounting that goes on the result, nil when nothing was
// held back.
func (in Input) heldSummary() *HeldDiffs {
	files := in.heldFiles()
	if len(files) == 0 {
		return nil
	}
	total := 0
	for _, f := range in.Change.Files {
		if f.Diff != "" || f.Head != "" {
			total++
		}
	}
	h := &HeldDiffs{Total: total}
	for _, f := range files {
		h.Held = append(h.Held, f.Path)
	}
	return h
}

// fetchDiff answers a get_diff call and says which held path it served, empty
// when it served none.
func fetchDiff(diffs map[string]string, input json.RawMessage) (answer, path string) {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &in)
	p := strings.TrimSpace(in.Path)
	if len(diffs) == 0 {
		return "No diffs are held back in this review; every diff is in the prompt.", ""
	}
	if d, ok := diffs[p]; ok {
		return d, p
	}
	return fmt.Sprintf("%s: no held-back diff for that path. Use a path from the held-back list "+
		"exactly as written; a file whose diff is shown in the prompt is not held back.", p), ""
}

// noteDiffsRead adds paths the reviewer read with get_diff to the accounting,
// once each.
func (r *Result) noteDiffsRead(paths []string) {
	if r.Held == nil {
		return
	}
	for _, p := range paths {
		if !contains(r.Held.Read, p) {
			r.Held.Read = append(r.Held.Read, p)
		}
	}
}

// heldRead is the paths read so far, nil when nothing was held back.
func (r *Result) heldRead() []string {
	if r.Held == nil {
		return nil
	}
	return r.Held.Read
}
