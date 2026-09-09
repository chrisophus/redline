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

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
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
  --covered ROLES   comma-separated roles another provider resolves exactly,
                    which this one will not record (for example
                    enclosing,caller,type,sibling,history beside gorefactor)
  --covered-scope GLOBS  the files that applies to (for example **/*.go)
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
	// Usage cannot report a write failure to anyone, and a flag parse is
	// already going wrong when it runs.
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, usage) }
	var (
		changed     = fs.String("changed", "", "merge-base revision")
		dir         = fs.String("dir", ".", "tree under review")
		graph       = fs.String("graph", "", "Graphify graph")
		model       = fs.String("model", "", "model to scout with")
		effort      = fs.String("effort", "", "effort level")
		maxCost     = fs.Float64("max-cost", 0, "stop looking above this many dollars")
		maxTurns    = fs.Int("max-turns", 0, "turn limit")
		covered     = fs.String("covered", "", "roles another provider already resolves exactly")
		coverScope  = fs.String("covered-scope", "", "globs those roles are covered for")
		baseURL     = fs.String("base-url", "", "endpoint, for a proxy")
		showVersion = fs.Bool("version", false, "print the adapter version")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintf(stdout, "redline-scout %s\n", version)
		return err
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

	generated := generatedIn(repo, paths)
	opts := scout.Options{
		Root:         repo.Root,
		Changed:      paths,
		Generated:    generated,
		BaseSHA:      base,
		Diff:         diffOf(repo, base, paths, generated),
		Graph:        resolveGraph(repo.Root, *graph),
		Model:        *model,
		Effort:       *effort,
		MaxTurns:     *maxTurns,
		MaxCostUSD:   *maxCost,
		Covered:      coveredRoles(*covered),
		CoveredScope: commaList(*coverScope),
		BaseURL:      *baseURL,
		APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
	}
	env, spend, err := scout.Run(context.Background(), opts)
	if err != nil {
		return err
	}
	// The spend is stated on every run, not only when something goes wrong.
	// This provider is the one that costs money, and a cost nobody sees is a
	// cost nobody governs. The error is dropped because a diagnostic line that
	// cannot be written is not worth failing a review that already succeeded.
	_, _ = fmt.Fprintf(stderr, "redline-scout: %d turn(s), %d record(s), %d in / %d out tokens, %s%s\n",
		spend.Turns, spend.Records, spend.Usage.InputTokens, spend.Usage.OutputTokens,
		review.FormatCost(spend.CostUSD, spend.CostKnown), capNote(spend))

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// coveredRoles reads the roles another provider already resolves. Naming them
// here rather than hard-coding gorefactor keeps the scout ignorant of which
// providers exist, the same way Redline is.
func coveredRoles(s string) []envelope.Role {
	var out []envelope.Role
	for _, r := range commaList(s) {
		out = append(out, envelope.Role(r))
	}
	return out
}

// commaList reads a comma-separated flag, dropping the empty entries a
// trailing comma or an unset flag leaves behind.
func commaList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func capNote(s scout.Spend) string {
	if s.CapHit {
		return " (stopped at the cost cap)"
	}
	return ""
}

// diffOf is the change as the reviewer will see it, which is the scout's
// brief. It holds back what Redline holds back from the review itself, for
// the same two reasons.
//
// Test files, because Redline keeps test context out of the review, so a
// scout that spent turns reading them would be working on material the
// reviewer is never shown.
//
// Generated files, because a regenerated protobuf or a lockfile is tens of
// thousands of lines of machine output that no reviewer acts on. Sending it
// would spend the scout's context and its cost cap on the one part of the
// change that cannot carry a finding, and on a large regeneration it would
// crowd out the hand-written lines entirely.
//
// Neither is dropped silently. Both are named with their counts, which is the
// same bargain the review prompt strikes: the scout can see that a file moved
// and decide whether that matters, without being handed its contents.
func diffOf(repo *gitx.Repo, base string, paths []string, generated map[string]string) string {
	counts := map[string]gitx.DiffStat{}
	stats, err := repo.Stat(base, "")
	if err == nil {
		for _, st := range stats {
			counts[st.Path] = st
		}
	}
	var b strings.Builder
	for _, p := range paths {
		st := counts[p]
		if reason := generated[p]; reason != "" {
			fmt.Fprintf(&b, "--- %s (generated: %s, +%d -%d, not shown)\n\n", p, reason, st.Added, st.Removed)
			continue
		}
		if isTestPath(p) {
			fmt.Fprintf(&b, "--- %s (test file, +%d -%d, not shown)\n\n", p, st.Added, st.Removed)
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

// generatedIn classifies the change the way Redline does, so the scout and
// the review agree about what is machine output. The detection is
// deliberately conservative on Redline's side because hiding a hand-written
// file is worse than showing a generated one, and that judgement is reused
// here rather than guessed at again from file names.
func generatedIn(repo *gitx.Repo, paths []string) map[string]string {
	attrs := repo.AttrSet("linguist-generated", paths)
	out := map[string]string{}
	for _, p := range paths {
		if reason := change.GeneratedReason(repo.Root, p, attrs); reason != "" {
			out[p] = reason
		}
	}
	return out
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
