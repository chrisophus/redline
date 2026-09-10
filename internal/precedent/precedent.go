// Package precedent puts the file next door in front of the reviewer.
//
// The failure it is built for: a review flagged a hard-coded column list, an
// idempotency check outside its transaction, and a zero-based row index in a
// new feed staging file. All three were accurate. All three were dismissed,
// because the file beside it in the same directory had made the same choice
// and the team had accepted it. The convention was written down nowhere. It
// was in the code, one directory listing away, and the reviewer could not see
// it.
//
// Nothing shipping answers that. gorefactor's sibling role is another
// implementation of a declared interface, and two staging files that share no
// interface are not siblings to a type checker. The parity pane compares
// filenames across parallel directories, which is the other axis: it answers
// "gcp has this and azure does not", not "the file next to this one solved
// the same problem already".
//
// So this reads the directory. Two files whose names share enough of their
// parts are doing the same kind of work, and the older one is the precedent
// the newer one follows. That is a guess from a filename and it is stated as
// one: the expansion says it was matched by name, and the prompt fragment
// tells the reviewer to read it as a neighbouring file rather than as a rule.
// It is worth making because the guess is cheap, deterministic, and right
// often enough that the reviewer stops inventing a convention the repository
// already has.
//
// A second field round showed the same failure one directory further out. A
// review of offerfeedingest/workflow.go flagged its checkpoint encoding, its
// parse-fail skip and its empty schedule request as novel, and every one of
// them was answered by accountfeedingest/workflow.go: the same filename in the
// package next door, which is not the file beside it and which nothing carried.
// So there are two strategies. Beside it is the stronger claim and is tried
// first; the same name one package over is the fallback, and it fires only
// when the two directories share enough filenames to be the same thing built
// twice, which is the parity pane's own test.
package precedent

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/pane/parity"
)

// ProviderName is what the report and the prompt call this context.
const ProviderName = "precedent"

// minTokens is how many parts a name needs before it can match anything.
//
// A one-word name carries no shape. In a package of delta.go, config.go and
// tools.go every pair shares nothing, which is correct, and a rule that let
// single tokens match would pair them on their extension alone.
const minTokens = 2

// maxCandidates is how many name-siblings a changed file may have before this
// says nothing about it.
//
// The guard that makes the heuristic safe. When one file in a directory of
// thirty shares its shape with three others, those three mean something. When
// it shares its shape with twenty, the token they have in common is the
// directory's own vocabulary -- handler, service, test -- and picking one of
// the twenty would be picking arbitrarily. Silence is the honest answer, and
// it is the answer a reviewer can act on: nothing was claimed.
const maxCandidates = 3

// maxSiblings bounds the whole envelope. A change touching forty files must
// not put forty whole files in front of a reviewer whose ceiling is spent on
// the diff.
const maxSiblings = 3

// maxLines bounds one sibling. The point is the shape of the thing, which the
// top of a file carries; a reviewer who needs the rest has the repository.
const maxLines = 500

// Resolve reads the precedent for this change. No sibling worth naming
// returns a nil envelope and no error, which is the common answer and is not
// a failure to look.
func Resolve(root string, changed []string) (*envelope.Envelope, error) {
	inChange := make(map[string]bool, len(changed))
	for _, p := range changed {
		inChange[normPath(p)] = true
	}

	var found []sibling
	seen := map[string]bool{}
	dirs := newDirCache(root)
	for _, p := range changed {
		rel := normPath(p)
		if !worthAsking(rel) {
			continue
		}
		// Beside it first, then the same name one package over. The first is
		// the stronger claim: two files in one directory whose names rhyme
		// are doing the same job by the author's own arrangement, where a
		// parallel package is an inference from two directory listings.
		s, ok, err := nearest(dirs, rel, inChange)
		if err != nil {
			return nil, err
		}
		if !ok {
			s, ok, err = parallel(dirs, rel, inChange)
			if err != nil {
				return nil, err
			}
		}
		if !ok || seen[s.path] {
			continue
		}
		seen[s.path] = true
		found = append(found, s)
	}
	if len(found) == 0 {
		return nil, nil
	}
	// Best first, so the bound below keeps the strongest match rather than
	// whichever changed file git happened to list first.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].shared != found[j].shared {
			return found[i].shared > found[j].shared
		}
		return found[i].path < found[j].path
	})
	omitted := 0
	if len(found) > maxSiblings {
		omitted = len(found) - maxSiblings
		found = found[:maxSiblings]
	}

	env := &envelope.Envelope{
		SchemaVersion:  envelope.SchemaVersion,
		Provider:       envelope.Provider{Name: ProviderName, Version: "builtin"},
		PromptFragment: promptFragment,
	}
	for _, s := range found {
		content, lines, truncated, err := read(root, s.path)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		symbol := fmt.Sprintf("%s, the file beside %s", path.Base(s.path), path.Base(s.forFile))
		found := "name"
		if s.parallel {
			symbol = fmt.Sprintf("%s, the same file in the package beside %s",
				s.path, path.Dir(s.forFile))
			found = "parallel-package"
		}
		x := envelope.Expansion{
			Role:      envelope.RoleSibling,
			Priority:  priority,
			Symbol:    symbol,
			File:      s.path,
			StartLine: 1,
			EndLine:   lines,
			Content:   content,
			Details: map[string]string{
				// Provenance the reader can check. A block that says it was
				// resolved is a different claim from one that says it was
				// guessed at from a filename, and this is the second.
				"found_via": found,
				"beside":    s.forFile,
			},
		}
		if truncated {
			x.Details["span"] = "truncated"
		}
		env.Expansions = append(env.Expansions, x)
	}
	if len(env.Expansions) == 0 {
		return nil, nil
	}
	if omitted > 0 {
		env.Notes = append(env.Notes, fmt.Sprintf(
			"%d further file(s) beside the changed ones were found by name and are not carried", omitted))
	}
	return env, nil
}

// priority ranks a precedent against the other siblings a provider resolved.
// Below a type checker's, which is a resolved fact where this is a guess, and
// above nothing: when the budget binds, the exact answer should survive and
// the guess should not.
const priority = 40

// promptFragment says what this context is and, more importantly, what it is
// not. A block of real source under a role reads as established fact, and
// half of what makes this block relevant is a guess about a filename.
const promptFragment = `The context tagged precedent is code this repository
already has that looks like the code being changed. Either the file sitting
beside a changed one, whose name shares enough parts with it, or the same
filename in a package next door that is built the same way. The block says
which. The code is real, copied from the repository at the revision under
review. The claim that it is related is a guess from a filename and nothing
more.

Read it as what this repository already does. When the change makes the same
choice its neighbour made, that choice is this team's convention, whether or
not anyone wrote it down, and a finding against it is wrong: it is a finding
against the neighbour too, and against every review that accepted it. Say
nothing, or say something about both.

That does not make the neighbour correct. A defect the change copies from the
file beside it is still a defect, and it is worth more to say so once, naming
both, than to report the copy as though it were new. What is never worth
saying is that the change should have done it the other way, with no account
of why the repository does it this way everywhere else.`

// sibling is one precedent and what it was found for.
type sibling struct {
	path    string
	forFile string
	shared  int
	// parallel records that this came from the directory next door rather
	// than from beside the changed file. The reader is told which, because
	// the two are different strengths of claim.
	parallel bool
}

// worthAsking reports whether a changed file is one a precedent would help
// with. Test files and generated files are held back from the review anyway,
// so a precedent for one is budget spent on something nobody will read.
func worthAsking(rel string) bool {
	if change.IsTest(rel) {
		return false
	}
	return path.Ext(rel) != ""
}

// nearest finds the one file in the same directory that best matches a
// changed file's name, or reports that the directory does not single one out.
func nearest(dirs *dirCache, rel string, inChange map[string]bool) (sibling, bool, error) {
	dir := path.Dir(rel)
	base := path.Base(rel)
	ext := path.Ext(base)
	want := tokens(base)
	if len(want) < minTokens {
		return sibling{}, false, nil
	}

	entries, err := dirs.files(dir)
	if err != nil {
		return sibling{}, false, err
	}

	var cands []sibling
	for _, name := range entries {
		if name == base || path.Ext(name) != ext {
			continue
		}
		other := tokens(name)
		if len(other) < minTokens {
			continue
		}
		n := sharedTokens(want, other)
		if n < required(len(want), len(other)) {
			continue
		}
		p := name
		if dir != "." {
			p = dir + "/" + name
		}
		if change.IsTest(p) {
			continue
		}
		cands = append(cands, sibling{path: p, forFile: rel, shared: n})
	}
	// Counted before the change's own files are dropped. A directory whose
	// naming does not single anything out is the case this bails on, and
	// whether some of those files happen to be in this change does not change
	// that fact about the directory.
	if len(cands) == 0 || len(cands) > maxCandidates {
		return sibling{}, false, nil
	}
	var kept []sibling
	for _, c := range cands {
		// Already in front of the reviewer as part of the diff. Sending it
		// again would spend the ceiling to repeat what was shown.
		if !inChange[c.path] {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return sibling{}, false, nil
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].shared != kept[j].shared {
			return kept[i].shared > kept[j].shared
		}
		return kept[i].path < kept[j].path
	})
	return kept[0], true, nil
}

// required is how many parts two names must share to count as the same kind
// of thing: half the shorter name, rounded up.
//
// Half rather than a constant, because it has to scale. One shared part out of
// two is offer_feed beside account_feed, which is the case this exists for.
// One shared part out of five is two files that happen to both mention aws,
// which is not.
func required(a, b int) int {
	n := a
	if b < n {
		n = b
	}
	return (n + 1) / 2
}

// tokens splits a filename into its parts, lowercased, dropping the
// extension. Underscores, hyphens and dots are all used as separators in
// practice and none of them means anything different here.
func tokens(base string) []string {
	name := strings.TrimSuffix(base, path.Ext(base))
	fields := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	})
	seen := map[string]bool{}
	out := fields[:0]
	for _, f := range fields {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func sharedTokens(a, b []string) int {
	in := make(map[string]bool, len(a))
	for _, t := range a {
		in[t] = true
	}
	n := 0
	for _, t := range b {
		if in[t] {
			n++
		}
	}
	return n
}

// read loads a sibling, bounded.
func read(root, rel string) (content string, lines int, truncated bool, err error) {
	buf, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "", 0, false, nil
		}
		return "", 0, false, fmt.Errorf("reading %s: %w", rel, rerr)
	}
	all := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(all) <= maxLines {
		return strings.Join(all, "\n"), len(all), false, nil
	}
	return strings.Join(all[:maxLines], "\n"), maxLines, true, nil
}

func normPath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(p), "./")
}

// dirCache lists a directory once. Both strategies read the same handful of
// directories repeatedly on a wide change, and a parallel-package lookup reads
// every sibling of a parent to decide which ones are siblings at all.
type dirCache struct {
	root  string
	byDir map[string][]string
}

func newDirCache(root string) *dirCache {
	return &dirCache{root: root, byDir: map[string][]string{}}
}

// files lists a directory's file names, without its subdirectories. A missing
// directory is empty rather than an error: git lists paths the tree may not
// hold, and a review must not fail because one of them has gone.
func (d *dirCache) files(dir string) ([]string, error) {
	if got, ok := d.byDir[dir]; ok {
		return got, nil
	}
	entries, err := os.ReadDir(filepath.Join(d.root, filepath.FromSlash(dir)))
	if err != nil {
		if os.IsNotExist(err) {
			d.byDir[dir] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	d.byDir[dir] = out
	return out, nil
}

// dirs lists a directory's subdirectories.
func (d *dirCache) dirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.root, filepath.FromSlash(dir)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// parallel finds the same file one package over.
//
// The second field round is what this is for. A review of
// offerfeedingest/workflow.go flagged its checkpoint encoding, its parse-fail
// skip and its empty schedule request as though each were novel, and
// accountfeedingest/workflow.go had answered all three. Those are not the file
// beside it: they are the same name in the directory next door, which nothing
// shipping put in front of a reviewer. The parity pane knows those two
// directories are parallel, and reports the file one of them is missing; it
// does not carry the sibling's body. gorefactor still needs a shared
// interface. The scout could find it, but only if the generator happened to
// ask a precedent question naming the pattern, and on that change it did not.
//
// Two directories are parallel when they sit under one parent and share
// filenames, which is the parity pane's own test and its own threshold. Two
// packages that merely sit side by side share nothing, so a change to one says
// nothing about the other and this stays quiet.
func parallel(dirs *dirCache, rel string, inChange map[string]bool) (sibling, bool, error) {
	dir := path.Dir(rel)
	base := path.Base(rel)
	parent := path.Dir(dir)
	if dir == "." || parent == "." || parent == dir {
		// A file at the root, or one whose directory is the root's own child,
		// has no parent to hold parallel implementations.
		return sibling{}, false, nil
	}
	mine, err := dirs.files(dir)
	if err != nil {
		return sibling{}, false, err
	}
	names := make(map[string]bool, len(mine))
	for _, n := range mine {
		names[n] = true
	}

	kids, err := dirs.dirs(parent)
	if err != nil {
		return sibling{}, false, err
	}
	var best sibling
	var found bool
	for _, kid := range kids {
		other := parent + "/" + kid
		if other == dir {
			continue
		}
		theirs, err := dirs.files(other)
		if err != nil {
			return sibling{}, false, err
		}
		var shared int
		var hasBase bool
		for _, n := range theirs {
			if names[n] {
				shared++
			}
			if n == base {
				hasBase = true
			}
		}
		// The same name has to be there, and the two directories have to look
		// like the same thing built twice. Either alone is a coincidence: a
		// doc.go in every package, or two packages that share a utils.go.
		if !hasBase || shared < parity.MinShared {
			continue
		}
		p := other + "/" + base
		if inChange[p] || change.IsTest(p) {
			continue
		}
		if !found || shared > best.shared || (shared == best.shared && p < best.path) {
			best, found = sibling{path: p, forFile: rel, shared: shared, parallel: true}, true
		}
	}
	return best, found, nil
}
