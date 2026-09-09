// Command redline-graphify-context is a context provider backed by a
// Graphify graph.
//
// Redline runs it as a subprocess in the tree under review and reads one
// context envelope from its stdout, the same contract every provider serves
// (docs/context-envelope.md). It resolves nothing itself: it reads the graph
// tree-sitter already wrote at graphify-out/graph.json, works out which nodes
// the changed files own, walks one hop, and reports what it found and what it
// could not follow.
//
// The point of it is reach. The Go provider resolves through go/types, so it
// is exact and it knows only Go; the graph spans about thirty-seven grammars,
// including the SQL, Terraform and shell files a change touches that no
// single language toolchain sees. The two are not competitors, which is why
// --defer-callers exists: where an exact resolver already answers "who calls
// this", it wins.
//
// Configure it in .redline.yml:
//
//	context:
//	  - name: graphify
//	    command: redline-graphify-context
//	    args: ["--graph", "graphify-out/graph.json", "--changed", "{{base}}",
//	           "--defer-callers", "**/*.go"]
//	    scope: ["**/*"]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/graphify"
)

// version is stamped at build time, the same way redline's is. It identifies
// the adapter; the graph's own build revision goes in the envelope's
// provider.version, because that is the input a replayed review has to be
// able to name.
var version = "dev"

const usage = `redline-graphify-context — a context provider backed by a Graphify graph

usage:
  redline-graphify-context --changed BASE [flags]

flags:
  --changed REF        merge-base revision the change is measured against (required)
  --graph PATH         graph.json to read (default graphify-out/graph.json)
  --dir PATH           tree under review (default .)
  --defer-callers GLOB comma-separated globs whose callers another provider
                       resolves exactly; no caller expansion is emitted for them
  --max-lines N        longest span one expansion may carry (default 60)
  --version            print the adapter version

It writes one context envelope as JSON to stdout and exits 0. A missing or
unreadable graph is an error: Redline records a provider that did not run,
which is not the same thing as one that found nothing.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "redline-graphify-context: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("redline-graphify-context", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var (
		changed      = fs.String("changed", "", "merge-base revision")
		graphPath    = fs.String("graph", filepath.Join("graphify-out", "graph.json"), "graph.json to read")
		dir          = fs.String("dir", ".", "tree under review")
		deferCallers = fs.String("defer-callers", "", "globs whose callers another provider resolves")
		maxLines     = fs.Int("max-lines", 0, "longest span one expansion may carry")
		showVersion  = fs.Bool("version", false, "print the adapter version")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintf(stdout, "redline-graphify-context %s\n", version)
		return nil
	}
	if strings.TrimSpace(*changed) == "" {
		fs.Usage()
		return fmt.Errorf("--changed is required")
	}

	repo, err := gitx.Open(*dir)
	if err != nil {
		return err
	}
	base, err := repo.Resolve(*changed)
	if err != nil {
		return err
	}
	// The changed set is computed the way Redline computes it, from the same
	// package, so the provider speaks about exactly the files the review is
	// about — untracked files and deletions included.
	paths, err := repo.ChangedPaths(base)
	if err != nil {
		return fmt.Errorf("changed paths: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		// Not fatal. Without it the staleness note cannot be written, and the
		// envelope says so rather than the run failing.
		head = ""
	}

	resolved := resolveGraphPath(repo.Root, *graphPath)
	graph, err := graphify.Load(resolved)
	if err != nil {
		return err
	}
	env := graph.Envelope(graphify.Options{
		Root:         repo.Root,
		GraphPath:    relativeTo(repo.Root, resolved),
		Changed:      withoutGraph(paths, repo.Root, resolved),
		BaseSHA:      base,
		HeadSHA:      head,
		DeferCallers: splitGlobs(*deferCallers),
		MaxLines:     *maxLines,
	})

	enc := json.NewEncoder(stdout)
	// Code carries angle brackets and ampersands. Escaping them costs six
	// bytes each and makes the envelope unreadable to anyone debugging it;
	// the JSON is identical either way.
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// resolveGraphPath reads the graph from the repository root when a relative
// path was given. Redline runs a provider with the tree under review as the
// working directory, but a detached worktree for a pull request is not where
// the caller ran `graphify update`.
func resolveGraphPath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// relativeTo puts the graph path in the form the staleness check wants: one
// relative to the repository root, or the path unchanged when it lies
// outside.
func relativeTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// withoutGraph drops the graph itself from the change. A repository that
// commits graphify-out/ rather than ignoring it puts the artifact in every
// diff that rebuilt it, and reporting "the graph holds no nodes for
// graph.json" on those reviews is noise about the provider's own input rather
// than about the change under review.
func withoutGraph(paths []string, root, graphPath string) []string {
	rel, err := filepath.Rel(root, graphPath)
	if err != nil {
		return paths
	}
	rel = filepath.ToSlash(rel)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.ToSlash(p) == rel {
			continue
		}
		out = append(out, p)
	}
	return out
}

func splitGlobs(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}
