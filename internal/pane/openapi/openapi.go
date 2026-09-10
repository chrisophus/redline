// Package openapi observes an OpenAPI specification at two revisions and
// reports what moved on the contract.
//
// A reviewer always reads the spec diff, and reads it for one question that the
// raw diff answers badly: did anything here break a client? A removed
// operation, a removed response code, and a newly required input are all
// breaking, and all three look like ordinary indented YAML in a diff. Naming
// them is the whole job of this pane.
//
// What it does not do is decide whether the handler matches the spec, or
// whether the deployed service agrees with either. Those need a running system
// and are a later pane.
package openapi

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/pane"
	"gopkg.in/yaml.v3"
)

// Name is the substrate these findings are recorded under.
const Name = "redline/api"

// Pane diffs OpenAPI specs between two revisions.
type Pane struct {
	Repo *gitx.Repo
	// Paths is the set of spec files in the change, set by Scope.
	Paths []string
}

func (p *Pane) Name() string { return Name }

// Scope claims the OpenAPI documents in the change. A spec is recognised by
// name rather than by parsing every YAML file in the repository: a Helm values
// file is also YAML with a `paths` key, and mistaking one for a contract would
// produce confident nonsense.
func (p *Pane) Scope(changed []string) []string {
	var out []string
	for _, path := range changed {
		if isSpec(path) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	p.Paths = out
	return out
}

func isSpec(path string) bool {
	lower := strings.ToLower(path)
	switch filepath.Ext(lower) {
	case ".yaml", ".yml", ".json":
	default:
		return false
	}
	base := filepath.Base(lower)
	return strings.Contains(base, "openapi") || strings.Contains(base, "swagger")
}

// Observation is the parsed contract at one revision.
type Observation struct {
	// Side is "base" or "worktree"; Rev is the revision it resolved to, empty
	// for the working tree.
	Side  string
	Rev   string
	Specs map[string]*spec
}

// ID implements pane.Observation.
func (o *Observation) ID() string { return Name + "@" + o.Side }

// Observe parses every in-scope spec at a revision. A spec that does not exist
// there is absent from the map, which is how an added or deleted document is
// represented — not as an error.
func (p *Pane) Observe(rev pane.Revision) (pane.Observation, error) {
	obs := &Observation{Side: rev.Name, Rev: rev.Rev, Specs: map[string]*spec{}}
	for _, path := range p.Paths {
		raw, err := p.Repo.File(rev.Rev, path)
		if err != nil {
			// A failed read is not an absent spec. Recording the error on the
			// spec makes Diff name it unknown rather than read the missing side
			// as an added or deleted document.
			obs.Specs[path] = &spec{Err: err}
			continue
		}
		if strings.TrimSpace(raw) == "" {
			continue
		}
		s, err := parse(raw)
		if err != nil {
			// An unparseable spec is reported as an unknown by Diff rather
			// than failing the run: half a review is worth more than none, and
			// a spec mid-edit is a normal state for a pre-push tool to meet.
			obs.Specs[path] = &spec{Err: err}
			continue
		}
		obs.Specs[path] = s
	}
	return obs, nil
}

// spec is the part of an OpenAPI document this pane compares.
type spec struct {
	Ops map[string]*operation
	Err error
	// UsesRef is set when the paths section references a definition with
	// $ref. This pane reads inline operations only and does not follow one,
	// so a spec that uses $ref cannot be confirmed as non-breaking.
	UsesRef bool
}

// operation is one method on one path, reduced to the things whose change
// breaks a caller.
type operation struct {
	Key       string
	Responses map[string]bool
	// Required is every input the caller must now supply: required parameters,
	// and required properties of the request body. Adding one breaks every
	// existing caller, which is the change most easily missed in a diff.
	Required map[string]bool
	// BodyRequired is whether the request body itself became mandatory.
	BodyRequired bool
}

var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// parse reads the operations out of a spec. JSON documents parse too: JSON is
// valid YAML, so one path handles both.
func parse(raw string) (*spec, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &root); err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := root.Decode(&doc); err != nil {
		return nil, err
	}
	s := &spec{Ops: map[string]*operation{}, UsesRef: containsRef(mapValue(&root, "paths"))}
	for path, item := range doc.Paths {
		for method, node := range item {
			lower := strings.ToLower(method)
			if !isMethod(lower) {
				continue
			}
			op := &operation{
				Key:       strings.ToUpper(lower) + " " + path,
				Responses: map[string]bool{},
				Required:  map[string]bool{},
			}
			readOperation(&node, op)
			s.Ops[op.Key] = op
		}
	}
	return s, nil
}

// mapValue returns the value node for key in a mapping, unwrapping a document
// node first. It returns nil when the node is not a mapping or lacks the key.
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return mapValue(n.Content[0], key)
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// containsRef reports whether the subtree has a $ref mapping key anywhere.
// A $ref points at a definition this pane does not resolve, so its presence
// means an operation, input, or response may be hidden from the diff.
func containsRef(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "$ref" {
				return true
			}
			if containsRef(n.Content[i+1]) {
				return true
			}
		}
		return false
	}
	for _, c := range n.Content {
		if containsRef(c) {
			return true
		}
	}
	return false
}

func isMethod(s string) bool {
	for _, m := range methods {
		if s == m {
			return true
		}
	}
	return false
}

func readOperation(node *yaml.Node, op *operation) {
	var body struct {
		Responses map[string]yaml.Node `yaml:"responses"`
		Params    []struct {
			Name     string `yaml:"name"`
			In       string `yaml:"in"`
			Required bool   `yaml:"required"`
		} `yaml:"parameters"`
		RequestBody struct {
			Required bool                 `yaml:"required"`
			Content  map[string]yaml.Node `yaml:"content"`
		} `yaml:"requestBody"`
	}
	if err := node.Decode(&body); err != nil {
		return
	}
	for code := range body.Responses {
		op.Responses[code] = true
	}
	for _, param := range body.Params {
		if param.Required && param.Name != "" {
			where := param.In
			if where == "" {
				where = "param"
			}
			op.Required[where+" "+param.Name] = true
		}
	}
	op.BodyRequired = body.RequestBody.Required
	for media, content := range body.RequestBody.Content {
		var c struct {
			Schema struct {
				Required []string `yaml:"required"`
			} `yaml:"schema"`
		}
		if err := content.Decode(&c); err != nil {
			continue
		}
		for _, field := range c.Schema.Required {
			op.Required[media+" body."+field] = true
		}
	}
}

// Diff reports what moved on the contract. Breaking changes are errors;
// additions are information a reviewer still wants, and a spec that moved
// without breaking anything is a confirmation rather than silence.
func (p *Pane) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*Observation)
	if !ok {
		return pane.Result{}, fmt.Errorf("openapi: base observation is %T", before)
	}
	head, ok := after.(*Observation)
	if !ok {
		return pane.Result{}, fmt.Errorf("openapi: head observation is %T", after)
	}

	res := pane.Result{Evidence: map[string]pane.Artifact{}}
	var lines []string

	for _, path := range p.Paths {
		b, h := base.Specs[path], head.Specs[path]
		switch {
		case h != nil && h.Err != nil:
			res.Unknowns = append(res.Unknowns, findings.Unknown{
				Substrate: Name,
				Message:   fmt.Sprintf("%s could not be parsed, so no contract change in it was checked", path),
				Reason:    h.Err.Error(),
			})
			continue
		case b != nil && b.Err != nil:
			res.Unknowns = append(res.Unknowns, findings.Unknown{
				Substrate: Name,
				Message:   fmt.Sprintf("%s could not be parsed at the base revision, so nothing was compared", path),
				Reason:    b.Err.Error(),
			})
			continue
		case h == nil && b == nil:
			continue
		case h == nil:
			res.Findings = append(res.Findings, finding(path, "api-spec-removed",
				findings.SeverityError,
				fmt.Sprintf("%s was deleted; every operation it declared is gone", path),
				fmt.Sprintf("%d operation(s) declared", len(b.Ops)), "no spec"))
			lines = append(lines, "spec removed: "+path)
			continue
		case b == nil:
			lines = append(lines, fmt.Sprintf("spec added: %s (%d operation(s))", path, len(h.Ops)))
			res.Confirmations = append(res.Confirmations, findings.Confirmation{
				Substrate: Name, Rule: "api-spec-added",
				Message: fmt.Sprintf("%s is new; %d operation(s) declared and nothing existing to break", path, len(h.Ops)),
			})
			continue
		}

		added, removed, changed := compare(b, h)
		for _, key := range removed {
			res.Findings = append(res.Findings, finding(path, "api-operation-removed",
				findings.SeverityError,
				fmt.Sprintf("%s was removed from the contract; callers of it will break", key),
				key+" declared", key+" absent"))
		}
		for _, c := range changed {
			res.Findings = append(res.Findings, c.finding(path))
		}
		for _, key := range added {
			lines = append(lines, "added: "+key)
		}
		for _, key := range removed {
			lines = append(lines, "removed: "+key)
		}
		for _, c := range changed {
			lines = append(lines, "changed: "+c.Key+" — "+c.what())
		}

		if diff := p.Repo.DiffPath(base.Rev, path); diff != "" {
			id := Name + ":" + path
			res.Evidence[id] = pane.Artifact{Kind: "diff", Content: diff}
			for i := range res.Findings {
				if res.Findings[i].File == path && len(res.Findings[i].Evidence) == 0 {
					res.Findings[i].Evidence = []string{id}
				}
			}
		}

		if len(removed) == 0 && breakingCount(changed) == 0 {
			if b.UsesRef || h.UsesRef {
				res.Unknowns = append(res.Unknowns, findings.Unknown{
					Substrate: Name,
					Message:   fmt.Sprintf("%s uses $ref, and this pane reads inline schemas only, so whether an input was made mandatory or a response dropped behind a $ref was not checked", path),
					Reason:    "spec uses $ref; inline schemas only",
				})
			} else {
				res.Confirmations = append(res.Confirmations, findings.Confirmation{
					Substrate: Name, Rule: "api-no-breaking-change",
					Message: fmt.Sprintf("%s changed, and nothing a caller depends on was removed or made mandatory (%d operation(s) added)", path, len(added)),
				})
			}
		}
	}

	res.Render = pane.Render{
		Title:   "API contract",
		Summary: summary(p.Paths, lines),
		Lines:   lines,
	}
	return res, nil
}

func summary(paths, lines []string) string {
	if len(paths) == 0 {
		return "No OpenAPI document changed."
	}
	if len(lines) == 0 {
		return fmt.Sprintf("%d spec file(s) changed with no operation-level difference.", len(paths))
	}
	return fmt.Sprintf("%d spec file(s) changed: %d operation-level difference(s).", len(paths), len(lines))
}

// change is one operation that exists on both sides but promises something
// different.
type change struct {
	Key              string
	RemovedResponses []string
	AddedResponses   []string
	NowRequired      []string
	NoLongerRequired []string
	BodyNowRequired  bool
}

func (c change) breaking() bool {
	return len(c.RemovedResponses) > 0 || len(c.NowRequired) > 0 || c.BodyNowRequired
}

func (c change) what() string {
	var parts []string
	if len(c.RemovedResponses) > 0 {
		parts = append(parts, "no longer returns "+strings.Join(c.RemovedResponses, ", "))
	}
	if c.BodyNowRequired {
		parts = append(parts, "request body is now mandatory")
	}
	if len(c.NowRequired) > 0 {
		parts = append(parts, "now requires "+strings.Join(c.NowRequired, ", "))
	}
	if len(c.AddedResponses) > 0 {
		parts = append(parts, "adds "+strings.Join(c.AddedResponses, ", "))
	}
	if len(c.NoLongerRequired) > 0 {
		parts = append(parts, "no longer requires "+strings.Join(c.NoLongerRequired, ", "))
	}
	return strings.Join(parts, "; ")
}

func (c change) finding(path string) findings.Finding {
	severity := findings.SeverityInfo
	rule := "api-operation-relaxed"
	if c.breaking() {
		severity = findings.SeverityError
		rule = "api-operation-breaking"
	}
	return finding(path, rule, severity,
		fmt.Sprintf("%s %s", c.Key, c.what()),
		"", "")
}

func breakingCount(changed []change) int {
	n := 0
	for _, c := range changed {
		if c.breaking() {
			n++
		}
	}
	return n
}

func compare(base, head *spec) (added, removed []string, changed []change) {
	for key := range head.Ops {
		if _, ok := base.Ops[key]; !ok {
			added = append(added, key)
		}
	}
	for key := range base.Ops {
		if _, ok := head.Ops[key]; !ok {
			removed = append(removed, key)
		}
	}
	for key, h := range head.Ops {
		b, ok := base.Ops[key]
		if !ok {
			continue
		}
		c := change{Key: key}
		for code := range b.Responses {
			if !h.Responses[code] {
				c.RemovedResponses = append(c.RemovedResponses, code)
			}
		}
		for code := range h.Responses {
			if !b.Responses[code] {
				c.AddedResponses = append(c.AddedResponses, code)
			}
		}
		for req := range h.Required {
			if !b.Required[req] {
				c.NowRequired = append(c.NowRequired, req)
			}
		}
		for req := range b.Required {
			if !h.Required[req] {
				c.NoLongerRequired = append(c.NoLongerRequired, req)
			}
		}
		c.BodyNowRequired = h.BodyRequired && !b.BodyRequired
		sort.Strings(c.RemovedResponses)
		sort.Strings(c.AddedResponses)
		sort.Strings(c.NowRequired)
		sort.Strings(c.NoLongerRequired)
		if c.what() != "" {
			changed = append(changed, c)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Slice(changed, func(i, j int) bool { return changed[i].Key < changed[j].Key })
	return added, removed, changed
}

func finding(path, rule string, sev findings.Severity, message, expected, observed string) findings.Finding {
	return findings.Finding{
		File:      path,
		Rule:      rule,
		Substrate: Name,
		Category:  findings.CategoryContract,
		Severity:  sev,
		Message:   message,
		Expected:  expected,
		Observed:  observed,
		Source:    findings.SourceDeterministic,
	}
}
