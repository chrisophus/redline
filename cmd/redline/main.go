// Command redline reviews a change: the working tree, a branch, or a GitHub
// pull request.
//
// The loop is three steps. `redline run` observes the change and reports what
// it can establish deterministically. `redline review` emits a packet of facts
// for the agent driving the session to judge. `redline ingest` takes the
// agent's judgments back, merges them into the report labelled as agent-
// authored, and opens the result.
//
// Redline makes no model calls of its own and never posts to GitHub.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
)

const usage = `redline — review a change with evidence, not just by reading it

usage:
  redline run     [flags]   observe the change and report (the entry point)
  redline review  [flags]   emit the review packet for the agent to judge
  redline ingest  [flags]   merge the agent's review back in and open the report
  redline open    [flags]   reopen the last report

target (all subcommands):
  --pr N|URL        review a GitHub pull request (read-only; uses gh)
  --branch REF      review a branch's tip
  (default)         review the working tree, uncommitted work included

flags:
  --base REF        base revision (default: the PR's base, else origin/main)
  --upstream REF    branch new migrations must not collide with
  --migrations DIR  restrict migration checks to one directory
  --format FMT      report|json  (default report)
  --out DIR         evidence directory (default .redline)
  --open            open the HTML report when done (default for ingest)
  --no-open         never open a browser
`

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redline:", err)
		os.Exit(1)
	}
}

type opts struct {
	base, upstream, migDir, format, out, pr, branch string
	open, noOpen                                    bool
}

func runMain(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd := args[0]
	fs := flag.NewFlagSet("redline "+cmd, flag.ContinueOnError)
	var o opts
	fs.StringVar(&o.base, "base", "", "base revision")
	fs.StringVar(&o.upstream, "upstream", "", "upstream branch for version-collision checks")
	fs.StringVar(&o.migDir, "migrations", "", "migrations directory")
	fs.StringVar(&o.format, "format", "report", "report|json")
	fs.StringVar(&o.out, "out", ".redline", "evidence directory")
	fs.StringVar(&o.pr, "pr", "", "GitHub pull request number or URL")
	fs.StringVar(&o.branch, "branch", "", "branch to review")
	fs.BoolVar(&o.open, "open", false, "open the HTML report when done")
	fs.BoolVar(&o.noOpen, "no-open", false, "never open a browser")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch cmd {
	case "run":
		return cmdRun(o)
	case "review":
		return cmdReview(o)
	case "ingest":
		return cmdIngest(o)
	case "open":
		return report.Open(filepath.Join(o.out, "report.html"))
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", cmd, usage)
	}
}

func (o opts) toRun(dir string) run.Options {
	return run.Options{Dir: dir, Base: o.base, Upstream: o.upstream,
		MigDir: o.migDir, PR: o.pr, Branch: o.branch, Out: o.out}
}

func cmdRun(o opts) error {
	res, err := execute(o)
	if err != nil {
		return err
	}
	if err := write(o, res, nil); err != nil {
		return err
	}
	if o.format == "json" {
		return emitJSON(res.Report)
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence))
	if o.open && !o.noOpen {
		return report.Open(filepath.Join(o.out, "report.html"))
	}
	return nil
}

// cmdReview emits the packet. Redline stops here: what it hands over is facts,
// and the judgment is the agent's. Nothing in this path talks to a model.
func cmdReview(o opts) error {
	res, err := execute(o)
	if err != nil {
		return err
	}
	if err := write(o, res, nil); err != nil {
		return err
	}
	return emitJSON(res.Packet)
}

// cmdIngest reads the agent's review from stdin and merges it in. Agent
// findings are stamped source "llm" and rendered apart from observed ones, so
// a reviewer always knows which half of the report reproduces.
func cmdIngest(o opts) error {
	rev, err := packet.ParseReview(os.Stdin)
	if err != nil {
		return err
	}
	res, err := run.LoadSession(o.out)
	if err != nil {
		return err
	}
	packet.Apply(&res.Report, rev)
	if err := write(o, res, rev); err != nil {
		return err
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence))
	if !o.noOpen {
		return report.Open(filepath.Join(o.out, "report.html"))
	}
	return nil
}

func execute(o opts) (*run.Result, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return run.Run(o.toRun(cwd))
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// write persists the run: findings.json, report.md, report.html, packet.json,
// and any captured artifacts. comments.json is never written here — it is the
// reviewer's own state, and merging the two would clobber their decisions on
// every re-run.
func write(o opts, res *run.Result, rev *packet.Review) error {
	dir := o.out
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "findings.json"), res.Report); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "packet.json"), res.Packet); err != nil {
		return err
	}
	if err := run.SaveSession(dir, res); err != nil {
		return err
	}
	md := report.Markdown(&res.Report, res.Renders, res.Evidence)
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	html, err := report.HTML(report.HTMLInput{
		Report: &res.Report, Packet: res.Packet, Review: rev,
		Renders: res.Renders, Evidence: res.Evidence,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte(html), 0o644); err != nil {
		return err
	}
	return writeArtifacts(dir, res.Evidence)
}

func writeArtifacts(dir string, evidence map[string]pane.Artifact) error {
	if len(evidence) == 0 {
		return nil
	}
	evDir := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		return err
	}
	for id, a := range evidence {
		if err := os.WriteFile(filepath.Join(evDir, report.EvidenceFile(id)), []byte(a.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(buf, '\n'), 0o644)
}
