// Package boundary holds the one check that keeps Redline out of the
// language-toolchain business.
//
// The architecture rests on a single claim: Redline is language-agnostic, and
// everything language-specific lives behind the provider interface. That claim
// is worth exactly as much as its enforcement. The first time someone needs a
// caller list and go/packages is one import away, the boundary erodes, and it
// erodes quietly because nothing fails.
//
// So it fails loudly here instead. This is the cheapest guard available.
package boundary

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// forbiddenPackages are import paths that would mean Redline is parsing a
// language rather than orchestrating a provider that does.
//
// go/token is deliberately absent: it is a position type, not a parser, and
// forbidding it would be a rule about spelling rather than about the
// boundary.
var forbiddenPackages = []string{
	"go/ast",
	"go/parser",
	"go/types",
	"go/importer",
	"go/build",
	"golang.org/x/tools",
}

// forbiddenModules are module paths Redline must not require. The plan's
// original wording was about go.mod, and it is still worth checking there:
// a module requirement is a deliberate act, and this is the line someone
// crosses on purpose.
var forbiddenModules = []string{
	"golang.org/x/tools",
	"golang.org/x/exp/typeparams",
}

type pkg struct {
	ImportPath  string
	Module      *struct{ Path string }
	Imports     []string
	TestImports []string
}

// TestRedlineParsesNoLanguage checks the packages Redline itself ships.
//
// The check is on first-party code rather than the whole dependency graph.
// A third-party library that reaches for go/parser inside its own
// implementation is not Redline resolving symbols, and failing on that would
// make the guard something people disable rather than something they respect.
// What matters is that no package in this module reaches a toolchain, whether
// directly or through another package in this module.
func TestRedlineParsesNoLanguage(t *testing.T) {
	pkgs := listPackages(t)
	var bad []string
	for _, p := range pkgs {
		if p.ImportPath == "github.com/chrisophus/redline/internal/boundary" {
			continue
		}
		for _, imp := range append(append([]string{}, p.Imports...), p.TestImports...) {
			if forbiddenPackage(imp) {
				bad = append(bad, p.ImportPath+" imports "+imp)
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("Redline reached a language toolchain:\n  %s\n\n%s",
			strings.Join(bad, "\n  "), rationale)
	}
}

// TestGoModRequiresNoLanguageToolchain is the second half. A package check
// alone would pass a module that requires x/tools and has not used it yet.
func TestGoModRequiresNoLanguageToolchain(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range forbiddenModules {
		if strings.Contains(string(data), m) {
			t.Fatalf("go.mod requires %s\n\n%s", m, rationale)
		}
	}
}

// providerOnlyPackages are packages that serve one provider's side of the
// envelope contract. They live in this module for now, and only the command
// that is that provider may import them.
//
// The rule is the same one the language check enforces, one layer out.
// internal/graphify knows Graphify's on-disk schema: which key holds the edge
// list, which marker says a model wrote an edge, how a node's line is spelled.
// Redline reading any of that directly would make the graph a thing Redline
// knows about rather than a provider it runs, and the first time it happened
// nothing would fail. So it fails here.
//
// It is also what keeps the split cheap. The adapter is a spike inside this
// repository; if it earns its own, this test says exactly what has to come
// with it.
var providerOnlyPackages = map[string][]string{
	"github.com/chrisophus/redline/internal/graphify": {
		"github.com/chrisophus/redline/cmd/redline-graphify-context",
	},
}

// TestProviderKnowledgeStaysBehindTheContract checks that a provider's own
// schema knowledge has exactly one consumer.
func TestProviderKnowledgeStaysBehindTheContract(t *testing.T) {
	pkgs := listPackages(t)
	var bad []string
	for _, p := range pkgs {
		for _, imp := range append(append([]string{}, p.Imports...), p.TestImports...) {
			allowed, restricted := providerOnlyPackages[imp]
			if !restricted || p.ImportPath == imp {
				continue
			}
			if !contains(allowed, p.ImportPath) {
				bad = append(bad, p.ImportPath+" imports "+imp)
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("a provider's own schema leaked into Redline:\n  %s\n\n%s",
			strings.Join(bad, "\n  "), providerRationale)
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

const providerRationale = "A context provider is a separate program and the envelope is the whole " +
	"interface. Redline must not read a provider's own file format, however " +
	"convenient it is that this one currently lives in the same module. " +
	"See docs/context-envelope.md."

const rationale = "Resolving symbols, callers and types belongs in a context provider, " +
	"which is a separate program invoked as a subprocess. See docs/context-envelope.md. " +
	"If a review needs something the envelope does not carry, add a field to the envelope " +
	"rather than a parser here."

func forbiddenPackage(imp string) bool {
	for _, f := range forbiddenPackages {
		if imp == f || strings.HasPrefix(imp, f+"/") {
			return true
		}
	}
	return false
}

func listPackages(t *testing.T) []pkg {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool is not on PATH")
	}
	out, err := exec.Command("go", "list", "-json", "../../...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []pkg
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages; the guard would pass vacuously")
	}
	return pkgs
}
