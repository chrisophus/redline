package scout

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/globmatch"
)

// RoleNeighbor is the cross-kind role the graph adapter introduced: a file of
// another kind the change is coupled to, which none of Redline's roles
// describes. The scout may use it for the same reason.
const RoleNeighbor = envelope.Role("neighbor")

// RoleGuideline is a rule the repository wrote down about itself: a house
// style, a convention, a decision record, the paragraph of a design doc that
// says why a thing is the way it is.
//
// It ships as a role Redline does not rank, the same way neighbor did, and
// for the same reason: the contract already keeps an unknown role and reports
// it, so the measurement decides whether it belongs in the vocabulary rather
// than the decision being made by writing it down first.
//
// What it is for is the review nobody wants: the one that says to use an em
// dash in a repository whose own style file forbids them, or to add a getter
// where the conventions file says not to. A finding that contradicts the
// house rules is wrong twice, because it is also evidence the tool did not
// read what the team wrote.
const RoleGuideline = envelope.Role("guideline")

// guidelineFloor keeps a rule from being the first thing dropped when the
// budget binds. Guideline and neighbor are both roles Redline does not rank,
// so they sort against each other on priority alone, and record order would
// otherwise decide it: the scout finds code first and reads the rules last,
// which is exactly backwards. A twenty-line rule the reviewer would otherwise
// contradict is worth more than the last adjacent file.
const guidelineFloor = 80

// allowedRoles is what the scout may tag an expansion with. A provider must
// not invent roles, and a model asked for a role will invent one cheerfully,
// so the set is enforced here rather than requested in the prompt.
var allowedRoles = map[envelope.Role]bool{
	envelope.RoleEnclosing: true,
	envelope.RoleCaller:    true,
	envelope.RoleType:      true,
	envelope.RoleSibling:   true,
	envelope.RoleHistory:   true,
	RoleNeighbor:           true,
	RoleGuideline:          true,
	// RoleTest is deliberately absent. Redline holds test expansions back per
	// review, so a scout turn spent finding one is a turn spent for nothing.
}

// foundVia is where the scout says it turned something up. A closed set, not
// free text: it is provenance the reader can check against the tools this
// program actually ran, and the moment it becomes a sentence it becomes the
// scout's opinion instead.
var foundVia = map[string]bool{
	"diff":       true,
	"grep":       true,
	"gorefactor": true,
	"graph":      true,
	"read":       true,
	"history":    true,
	"docs":       true,
}

// Limits bound what one scout run may put in an envelope. They are relevance
// bounds, not budget: Redline owns the token ceiling and does its own
// truncation, and a provider that trimmed to its own budget would throw away
// context Redline had room for. What these stop is a scout that records the
// whole repository one range at a time.
type Limits struct {
	MaxRecords    int
	MaxLines      int
	MaxHistoryLog int
}

func (l Limits) withDefaults() Limits {
	if l.MaxRecords <= 0 {
		l.MaxRecords = 40
	}
	if l.MaxLines <= 0 {
		l.MaxLines = 120
	}
	if l.MaxHistoryLog <= 0 {
		l.MaxHistoryLog = 3
	}
	return l
}

// record is one thing the scout asked to send. It carries no content: the
// content is read from the tree by resolve, which is the whole point of the
// split. The scout decides what the reviewer should see and the program
// decides what those bytes are, so a model that misremembers a function body
// cannot put its recollection in front of the reviewer as source.
type record struct {
	Role      envelope.Role
	File      string
	StartLine int
	EndLine   int
	Symbol    string
	FoundVia  string
	// Answers is the id of the question this record was fetched for, when the
	// scout was answering rather than exploring. Without it the ruling stage
	// gets a pile of code and no way to tell which claim any of it bears on,
	// which is most of what makes an answer an answer.
	Answers string
}

// resolver turns records into expansions by reading the tree.
type resolver struct {
	root   string
	limits Limits

	// covered is what another provider already resolves for coveredScope, so
	// a record under one of those roles is refused rather than paid for.
	covered      []envelope.Role
	coveredScope []string

	cache map[string][]string
	notes []string
}

func newResolver(root string, limits Limits) *resolver {
	return &resolver{root: root, limits: limits.withDefaults(), cache: map[string][]string{}}
}

// validate checks a record before it is accepted, and returns the reason it
// was refused. The scout is told the reason, so a misremembered path or a
// line past the end of the file becomes a correction it can act on rather
// than a silent hole in the context.
func (r *resolver) validate(rec record) error {
	if !allowedRoles[rec.Role] {
		return fmt.Errorf("role %q is not one Redline ranks; use enclosing, caller, type, sibling, history, neighbor or guideline", rec.Role)
	}
	if rec.FoundVia != "" && !foundVia[rec.FoundVia] {
		return fmt.Errorf("found_via %q is not one of diff, grep, gorefactor, graph, read, history, docs", rec.FoundVia)
	}
	if strings.TrimSpace(rec.File) == "" {
		return fmt.Errorf("no file")
	}
	if r.isCovered(rec.Role, rec.File) {
		return fmt.Errorf("%s is already resolved for %s by a provider that does it exactly; record something it cannot see instead",
			rec.Role, rec.File)
	}
	if classOf(rec.File) == envelope.ClassTest {
		// Redline drops test code from the context block, so a record here
		// would be paid for and never shown. Refused with the reason, which
		// is a correction the scout can act on; the prompt says the same
		// thing and a model told once will still sometimes do it.
		return fmt.Errorf("%s is a test file, and Redline holds test context back from the review; record the code it tests instead", rec.File)
	}
	lines, err := r.read(rec.File)
	if err != nil {
		return fmt.Errorf("%s: %w", rec.File, err)
	}
	if rec.StartLine < 1 {
		return fmt.Errorf("%s: start_line must be 1 or more", rec.File)
	}
	if rec.StartLine > len(lines) {
		return fmt.Errorf("%s has %d lines; start_line %d is past the end", rec.File, len(lines), rec.StartLine)
	}
	return nil
}

// isCovered reports whether another provider already resolves this role for
// this file. Told in the brief and enforced here: a model told not to do
// something will sometimes do it anyway, and the refusal is correctable
// because it comes back as an error result rather than ending the run.
func (r *resolver) isCovered(role envelope.Role, file string) bool {
	inScope := len(r.coveredScope) == 0 || globmatch.MatchesAny(r.coveredScope, normPath(file))
	if !inScope {
		return false
	}
	for _, c := range r.covered {
		if c == role {
			return true
		}
	}
	return false
}

// resolve reads the bytes for a record. Content only ever comes from here:
// the file on disk, or git's own output for history.
func (r *resolver) resolve(rec record, priority int) (envelope.Expansion, bool) {
	lines, err := r.read(rec.File)
	if err != nil {
		r.note(fmt.Sprintf("%s could not be read, so the %s context the scout asked for is missing: %v", rec.File, rec.Role, err))
		return envelope.Expansion{}, false
	}
	start, end := clamp(rec.StartLine, rec.EndLine, len(lines), r.limits.MaxLines)
	if start == 0 {
		return envelope.Expansion{}, false
	}
	details := map[string]string{}
	if rec.FoundVia != "" {
		details["foundVia"] = rec.FoundVia
	}
	if rec.Answers != "" {
		details["answers"] = rec.Answers
	}
	symbol := rec.Symbol
	if rec.FoundVia == "graph" || rec.FoundVia == "grep" {
		// The graph and grep match by name, not by type. The tag rides along
		// for the same reason it does in the graph provider: a name-resolved
		// connection must not read as a resolved fact. Nothing renders
		// details for the reviewer, so the header says it too: it is the one
		// line of a block the reviewer is certain to read.
		details["resolution"] = "name"
		symbol = strings.TrimSpace(symbol + " (matched by name)")
	}

	content := strings.Join(lines[start-1:end], "\n") + "\n"
	if rec.Role == envelope.RoleHistory {
		log, err := r.history(rec.File, start, end)
		if err != nil {
			r.note(fmt.Sprintf("git could not tell the history of %s:%d-%d, so that context is missing: %v", rec.File, start, end, err))
			return envelope.Expansion{}, false
		}
		content = log
	}
	if end-start+1 >= r.limits.MaxLines {
		details["span"] = "truncated"
	}
	return envelope.Expansion{
		Role:      rec.Role,
		Priority:  priority,
		Symbol:    symbol,
		File:      rec.File,
		StartLine: start,
		EndLine:   end,
		Content:   content,
		Details:   details,
	}, true
}

// Expansions turns the accepted records into the envelope's expansions, in a
// stable order. Priority descends with the order the scout recorded things,
// which is the only ranking signal it has: a scout that found the important
// thing first should not have it dropped for something it added as an
// afterthought. Redline still ranks role before priority, so this orders
// within a role and nothing else.
func (r *resolver) Expansions(records []record) []envelope.Expansion {
	out := make([]envelope.Expansion, 0, len(records))
	seen := map[string]bool{}
	for i, rec := range records {
		// The question is part of the key. One range can genuinely answer two
		// findings, and deduplicating those into one would leave the second
		// looking unchecked.
		key := fmt.Sprintf("%s:%s:%d:%d:%s", rec.Role, rec.File, rec.StartLine, rec.EndLine, rec.Answers)
		if seen[key] {
			continue
		}
		seen[key] = true
		if x, ok := r.resolve(rec, priorityFor(rec.Role, i)); ok {
			out = append(out, x)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, _ := a.Role.Rank()
		rb, _ := b.Role.Rank()
		switch {
		case ra != rb:
			return ra < rb
		case a.Priority != b.Priority:
			return a.Priority > b.Priority
		case a.File != b.File:
			return a.File < b.File
		default:
			return a.StartLine < b.StartLine
		}
	})
	return out
}

// priorityFor orders expansions within their role. Record order is the only
// ranking signal the scout gives, and it is a real one: it found the thing it
// went looking for first. Guidelines are the exception, for the reason
// guidelineFloor states.
func priorityFor(role envelope.Role, i int) int {
	p := 100 - i
	if p < 1 {
		p = 1
	}
	if role == RoleGuideline && p < guidelineFloor {
		p = guidelineFloor
	}
	return p
}

// Notes are what the resolver could not do. They join the scout's own notes
// in the envelope.
func (r *resolver) Notes() []string { return r.notes }

func (r *resolver) note(s string) {
	for _, existing := range r.notes {
		if existing == s {
			return
		}
	}
	r.notes = append(r.notes, s)
}

func (r *resolver) read(path string) ([]string, error) {
	clean := normPath(path)
	if lines, ok := r.cache[clean]; ok {
		if lines == nil {
			return nil, fmt.Errorf("no such file in the tree under review")
		}
		return lines, nil
	}
	full, err := r.inTree(clean)
	if err != nil {
		r.cache[clean] = nil
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		r.cache[clean] = nil
		return nil, fmt.Errorf("no such file in the tree under review")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	r.cache[clean] = lines
	return lines, nil
}

// inTree resolves a repository-relative path and refuses anything that climbs
// out of the tree under review. The scout names these paths, and a model that
// names ../../etc/passwd should get an error rather than a file. Lexical
// checks are not enough: a symlink added by the change can point a relative
// path at a file outside the tree, so the real path is resolved and checked
// too before its bytes reach an envelope.
func (r *resolver) inTree(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative to the repository root")
	}
	full := filepath.Join(r.root, filepath.FromSlash(rel))
	inside, err := filepath.Rel(r.root, full)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside the tree under review")
	}
	// A path that does not exist yet is not a symlink escape; let the
	// caller report it missing. Anything that does resolve must land inside
	// the tree's own real path.
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return full, nil
		}
		return "", fmt.Errorf("path is outside the tree under review")
	}
	root, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		root = r.root
	}
	realInside, err := filepath.Rel(root, resolved)
	if err != nil || realInside == ".." || strings.HasPrefix(realInside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path is outside the tree under review")
	}
	return full, nil
}

// history is git's account of the changed lines, copied as git printed it.
func (r *resolver) history(file string, start, end int) (string, error) {
	if _, err := r.inTree(normPath(file)); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "log",
		fmt.Sprintf("--max-count=%d", r.limits.MaxHistoryLog),
		"--no-color", "--patch",
		fmt.Sprintf("-L%d,%d:%s", start, end, normPath(file)))
	cmd.Dir = r.root
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git log -L failed")
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("no recorded history for those lines")
	}
	return string(out), nil
}

// clamp fits a requested span to the file and to the line bound. A scout that
// asks for lines 40 to 4000 of a 60-line file gets 40 to 60 rather than an
// error: the intent is clear and refusing it would cost a turn.
func clamp(start, end, fileLines, maxLines int) (int, int) {
	if start < 1 || fileLines == 0 || start > fileLines {
		return 0, 0
	}
	if end < start {
		end = start
	}
	if end > fileLines {
		end = fileLines
	}
	if end-start+1 > maxLines {
		end = start + maxLines - 1
	}
	return start, end
}

func normPath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "./")
}
