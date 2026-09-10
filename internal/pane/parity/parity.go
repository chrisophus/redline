// Package parity reports capabilities that exist for one sibling
// implementation and not the others.
//
// Repositories that support several providers, adapters or drivers usually
// keep them in parallel directories: internal/provider/{aws,azure,gcp},
// storage/{s3,gcs}, backends/{postgres,mysql}. When those siblings share no
// declared interface, nothing checks that a capability added to one exists in
// the rest. A type checker cannot: there is no interface to implement, so
// there is no unimplemented method to report. The graph cannot either, for the
// same reason, and it is why a real change adding
// internal/provider/gcp/offer_amend_mutability.go drew "no sibling
// expansions" from the exact provider that exists to answer that question.
//
// The question is answerable without any of that. A file called
// offer_amend_mutability.go under gcp, when aws and azure hold thirty of the
// same filenames and not that one, is a fact about the change that a reviewer
// wants. So this pane compares filenames across siblings and says nothing
// else. No parser, no model, no graph.
//
// It says nothing about symbols inside those files. Comparing exported names
// would need a per-language notion of what exported means, which is the
// knowledge Redline keeps behind the provider boundary, and the case that
// motivated this is a filename case.
package parity

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/pane"
)

// Substrate is the pane's name in the findings schema.
const Substrate = "redline/parity"

// MinShared is how many filenames two directories must have in common before
// this pane will call them parallel implementations of the same thing.
//
// This is the guard that keeps the pane quiet on directories that merely sit
// side by side. internal/pane/{lint,migrations,openapi} are siblings by path
// and share no filenames, so a change to one of them says nothing about the
// others and this pane holds its tongue. Provider directories share their
// whole shape, which is what makes a missing file there a signal.
//
// Exported because internal/precedent asks the same question for a different
// reason: this pane reports the file a sibling directory is missing, and that
// one puts the sibling's body in front of a review. Two answers to "are these
// parallel implementations" that could disagree would be worse than either.
const MinShared = 3

// Pane compares a changed file's directory against its siblings.
type Pane struct {
	Repo *gitx.Repo

	scoped []string
	byDir  map[string]map[string]bool
}

// Name implements pane.Pane.
func (p *Pane) Name() string { return Substrate }

// Scope selects the changed files that actually sit in a set of parallel
// implementations.
//
// Depth alone was the first cut and it was wrong in a way worth naming: the
// union of every pane's scope is how much of the change Redline says it
// examined, so a pane that claims a file it will not read makes the report
// state it looked at something it did not. Nearly every changed file is two
// directories deep, so that version emptied the "what could not be
// determined" section, which is the part of this report other tools leave
// out.
//
// Scoping to files whose directory has a parallel sibling costs one listing
// of the tree and makes the claim true.
func (p *Pane) Scope(changed []string) []string {
	byDir := p.tree()
	var out []string
	for _, f := range changed {
		if len(parallelSiblings(byDir, path.Dir(normalize(f)))) > 0 {
			out = append(out, f)
		}
	}
	p.scoped = out
	return out
}

// tree lists the working copy once per run. Scope is called again with every
// path in the repository to decide whether the pane applies at all, and a git
// call per invocation would be paid twice for the same answer.
func (p *Pane) tree() map[string]map[string]bool {
	if p.byDir != nil {
		return p.byDir
	}
	p.byDir = map[string]map[string]bool{}
	if p.Repo == nil {
		return p.byDir
	}
	blobs, err := p.Repo.WorktreeBlobs()
	if err != nil {
		return p.byDir
	}
	p.byDir = groupByDir(keys(blobs))
	return p.byDir
}

func normalize(p string) string { return path.Clean(strings.TrimPrefix(p, "./")) }

// observation is the file listing at one revision. The parity question is
// about what exists rather than about what changed inside a file, so the
// listing is the whole observation.
type observation struct {
	Rev   string
	Files []string
}

func (o *observation) ID() string { return "parity@" + o.Rev }

// Observe lists the tree at a revision.
func (p *Pane) Observe(rev pane.Revision) (pane.Observation, error) {
	files, err := p.list(rev)
	if err != nil {
		return nil, err
	}
	return &observation{Rev: rev.Rev, Files: files}, nil
}

func (p *Pane) list(rev pane.Revision) ([]string, error) {
	if rev.Rev == "" {
		blobs, err := p.Repo.WorktreeBlobs()
		if err != nil {
			return nil, err
		}
		return keys(blobs), nil
	}
	blobs, err := p.Repo.Blobs(rev.Rev)
	if err != nil {
		return nil, err
	}
	return keys(blobs), nil
}

// Diff reports the capabilities the change left uneven.
//
// It reads the head listing only. A file that was already missing from a
// sibling before this change is not this change's news, so the base listing
// decides which gaps are new: a gap that existed at base is left alone.
func (p *Pane) Diff(before, after pane.Observation) (pane.Result, error) {
	head, ok := after.(*observation)
	if !ok {
		return pane.Result{}, fmt.Errorf("parity: head observation is %T", after)
	}
	base, ok := before.(*observation)
	if !ok {
		return pane.Result{}, fmt.Errorf("parity: base observation is %T", before)
	}

	res := pane.Result{}
	byDir := groupByDir(head.Files)
	baseByDir := groupByDir(base.Files)
	var lines []string

	for _, changed := range p.scoped {
		dir, file := path.Dir(changed), path.Base(changed)
		// One sibling is enough. Two parallel implementations that share a
		// shape, where a capability landed in only one of them, is the whole
		// question; a repository does not need three providers to have it.
		siblings := parallelSiblings(byDir, dir)
		if len(siblings) == 0 {
			continue
		}
		var missing []string
		for _, sib := range siblings {
			if byDir[sib][file] {
				continue
			}
			// A gap that predates the change belongs to whoever made it.
			if !baseByDir[dir][file] {
				missing = append(missing, sib)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		res.Findings = append(res.Findings, findings.Finding{
			Substrate: Substrate,
			Rule:      "sibling-missing-file",
			Severity:  findings.SeverityInfo,
			File:      changed,
			Message: fmt.Sprintf("this change adds %s under %s; %s have no file of that name",
				file, path.Base(dir), strings.Join(shortNames(missing), " and ")),
			Observed: fmt.Sprintf("siblings checked: %s", strings.Join(shortNames(siblings), ", ")),
			Source:   findings.SourceDeterministic,
		})
		lines = append(lines, fmt.Sprintf("%s: absent from %s", changed, strings.Join(shortNames(missing), ", ")))
	}

	if len(res.Findings) == 0 {
		res.Confirmations = append(res.Confirmations, findings.Confirmation{
			Substrate: Substrate,
			Message:   "every changed file that sits in a set of parallel implementations has a counterpart in each sibling",
		})
		return res, nil
	}
	sort.Slice(res.Findings, func(i, j int) bool { return res.Findings[i].File < res.Findings[j].File })
	sort.Strings(lines)
	res.Render = pane.Render{
		Title:   "Provider parity",
		Summary: fmt.Sprintf("%d capability/ies added to one implementation and not its siblings", len(res.Findings)),
		Lines:   lines,
	}
	return res, nil
}

// parallelSiblings returns the directories beside dir that look like parallel
// implementations of the same thing, judged by how many filenames they share
// with it. A directory that shares nothing is a neighbour, not a sibling.
func parallelSiblings(byDir map[string]map[string]bool, dir string) []string {
	parent := path.Dir(dir)
	if parent == "." || parent == "/" || parent == dir {
		return nil
	}
	var out []string
	for other := range byDir {
		if other == dir || path.Dir(other) != parent {
			continue
		}
		if shared(byDir[dir], byDir[other]) >= MinShared {
			out = append(out, other)
		}
	}
	sort.Strings(out)
	return out
}

func shared(a, b map[string]bool) int {
	n := 0
	for name := range a {
		if b[name] {
			n++
		}
	}
	return n
}

func groupByDir(files []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, f := range files {
		dir := path.Dir(f)
		if out[dir] == nil {
			out[dir] = map[string]bool{}
		}
		out[dir][path.Base(f)] = true
	}
	return out
}

// shortNames trims sibling paths to their last segment, which is the name a
// reader thinks in: "azure and gcp", not two repository paths.
func shortNames(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		out[i] = path.Base(d)
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
