// Command redline-scout is a context provider that uses a model to decide
// what the reviewer needs.
//
// Redline runs it as a subprocess in the tree under review and reads one
// context envelope from its stdout, the same contract every provider serves
// (docs/context-envelope.md). Unlike the others it costs money: a cheap model
// reads the diff, uses a handful of tools to find the few things that bear on
// it, and records where they are. This program reads those bytes from the
// tree, so the reviewer gets real source under a selection a model made.
//
// It is the cheap half of a two-model split. The scout explores under a cost
// cap, and its findings are frozen into the envelope that the expensive model
// reads once. That keeps the review a single call over a fixed payload, which
// is what lets a saved session be replayed by the eval, while the exploring
// that would otherwise resend a large conversation to an expensive model
// happens at a fraction of the rate.
//
// Configure it in .redline.yml, and know that you are configuring an API
// call on every run:
//
//	context:
//	  - name: scout
//	    command: redline-scout
//	    args: ["--changed", "{{base}}", "--max-cost", "0.25"]
//	    scope: ["**/*"]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/gitx"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/scout"
)

var version = "dev"

const usage = `redline-scout — a context provider that decides what the reviewer needs

usage:
  redline-scout --changed BASE [flags]

flags:
  --changed REF     merge-base revision the change is measured against (required)
  --dir PATH        tree under review (default .)
  --graph PATH      Graphify graph, for the cross-language tools
                    (default graphify-out/graph.json when it exists)
  --model NAME      model to scout with (default claude-sonnet-5)
  --effort LEVEL    low|medium|high|xhigh|max (default low)
  --max-cost USD    stop looking when the next turn would exceed this (default 0.25)
  --max-turns N     turn limit (default 8)
  --base-url URL    endpoint, for a proxy
  --version         print the adapter version

It writes one context envelope as JSON to stdout and exits 0. It calls a
model, so it needs ANTHROPIC_API_KEY and it costs money on every run; what it
spent goes to stderr. Running out of budget is not a failure: it stops early,
says so in the envelope's notes, and the review still happens.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "redline-scout: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("redline-scout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	var (
		changed     = fs.String("changed", "", "merge-base revision")
		dir         = fs.String("dir", ".", "tree under review")
		graph       = fs.String("graph", "", "Graphify graph")
		model       = fs.String("model", "", "model to scout with")
		effort      = fs.String("effort", "", "effort level")
		maxCost     = fs.Float64("max-cost", 0, "stop looking above this many dollars")
		maxTurns    = fs.Int("max-turns", 0, "turn limit")
		baseURL     = fs.String("base-url", "", "endpoint, for a proxy")
		showVersion = fs.Bool("version", false, "print the adapter version")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Fprintf(stdout, "redline-scout %s\n", version)
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
	paths, err := repo.ChangedPaths(base)
	if err != nil {
		return fmt.Errorf("changed paths: %w", err)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no changed files against %s", base[:8])
	}

	opts := scout.Options{
		Root:       repo.Root,
		Changed:    paths,
		BaseSHA:    base,
		Diff:       diffOf(repo, base, paths),
		Graph:      resolveGraph(repo.Root, *graph),
		Model:      *model,
		Effort:     *effort,
		MaxTurns:   *maxTurns,
		MaxCostUSD: *maxCost,
		BaseURL:    *baseURL,
		APIKey:     os.Getenv("ANTHROPIC_API_KEY"),
	}
	env, spend, err := scout.Run(context.Background(), opts)
	if err != nil {
		return err
	}
	// The spend is stated on every run, not only when something goes wrong.
	// This provider is the one that costs money, and a cost nobody sees is a
	// cost nobody governs.
	fmt.Fprintf(stderr, "redline-scout: %d turn(s), %d record(s), %d in / %d out tokens, %s%s\n",
		spend.Turns, spend.Records, spend.Usage.InputTokens, spend.Usage.OutputTokens,
		review.FormatCost(spend.CostUSD, spend.CostKnown), capNote(spend))

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func capNote(s scout.Spend) string {
	if s.CapHit {
		return " (stopped at the cost cap)"
	}
	return ""
}

// diffOf is the change as the reviewer will see it, which is the scout's
// brief. Test files are left out of it: Redline holds test context back from
// the review, so a scout that spent turns reading tests would be working on
// material the reviewer is never shown.
func diffOf(repo *gitx.Repo, base string, paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		if isTestPath(p) {
			fmt.Fprintf(&b, "--- %s (test file, not shown)\n\n", p)
			continue
		}
		d := repo.DiffPath(base, p)
		if strings.TrimSpace(d) == "" {
			continue
		}
		b.WriteString(d)
		if !strings.HasSuffix(d, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func isTestPath(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	return strings.HasSuffix(base, "_test.go") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_")
}

// resolveGraph finds the Graphify graph when one is there. An absent graph is
// not an error: it turns off two tools and the scout is told so in its brief.
func resolveGraph(root, flagValue string) string {
	if flagValue != "" {
		if filepath.IsAbs(flagValue) {
			return flagValue
		}
		return filepath.Join(root, flagValue)
	}
	def := filepath.Join(root, "graphify-out", "graph.json")
	if st, err := os.Stat(def); err == nil && !st.IsDir() {
		return def
	}
	return ""
}
