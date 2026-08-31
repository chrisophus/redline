// Command redline reviews a change: the working tree, a commit, a commit
// range, a branch, or a GitHub pull request.
//
// The loop is three steps. `redline run` observes the change and reports what
// it can establish deterministically. `redline review` emits a packet of facts
// for the agent driving the session to judge. `redline ingest` takes the
// agent's judgments back, merges them into the report labelled as agent-
// authored, and opens the result.
//
// `redline post` is the one command that writes to GitHub: it submits the
// session's findings as one pull request review. It is always explicit — the
// other commands stay read-only — and it makes no model calls. Redline still
// composes no judgment of its own; it posts what the agent and its own
// deterministic panes found.
//
// It will execute a reviewer's own review command when asked to (--with), and
// label what comes back with that reviewer's name.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
	"github.com/ccason/redline/internal/target"
)

const usage = `redline — review a change with evidence, not just by reading it

usage:
  redline run     [flags]   observe the change and report (the entry point)
  redline review  [flags]   emit the review packet for the agent to judge
  redline ingest  [flags]   merge the agent's review back in and open the report
  redline post    [flags]   post the session's findings as one PR review (--pr)
  redline open    [flags]   serve and open the last report
  redline serve   [flags]   serve .redline over http (blocks; --stop ends it)

target (all subcommands; pass only one):
  (default)         working tree, uncommitted work included
  --commit REF      that commit against its parent (HEAD for the latest)
  --range A..B      commits reachable from B but not A (B defaults to HEAD)
  --branch REF      a branch's tip against --base
  --pr N|URL        a GitHub pull request (uses gh; read-only except post)

flags:
  --base REF        base revision (default: commit parent, range start, PR base, else origin/main)
  --upstream REF    branch new migrations must not collide with
  --migrations DIR  restrict migration checks to one directory
  --format FMT      report|json  (default report)
  --out DIR         evidence directory (default .redline)
  --open            open the HTML report when done (default for ingest)
  --no-open         never open a browser
  --port N          loopback port for open/serve (default 8765; the next
                    free port is used if it is taken)
  --with NAME       run an external reviewer and fold its findings in:
                    claude, cursor, none (default none). Repeat the flag or
                    comma-separate to run several — two reviewers from
                    different vendors is a second opinion, and each is
                    reported under its own name. --with none turns them all
                    off. Adapters are configurable in <out>/reviewers.json
  --report-url URL  with post: link to the full report in the review body
  --dry-run         with post: print the review payload instead of posting
  --stop            with serve: stop the server running for --out
`

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redline:", err)
		os.Exit(1)
	}
}

type opts struct {
	base, upstream, migDir, format, out, pr, branch, commit, revRange string
	with                                                              reviewerList
	reportURL                                                         string
	open, noOpen, stop, dryRun                                        bool
	port                                                              int
}

// reviewerList collects --with. It accepts the flag more than once and splits
// on commas, because two reviewers in one run is the point: a second opinion
// from a different vendor is what a second review pass used to be.
type reviewerList []string

func (l *reviewerList) String() string { return strings.Join(*l, ",") }

func (l *reviewerList) Set(v string) error {
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*l = append(*l, name)
		}
	}
	return nil
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
	fs.StringVar(&o.commit, "commit", "", "commit to review against its parent")
	fs.StringVar(&o.revRange, "range", "", "commit range A..B")
	fs.BoolVar(&o.open, "open", false, "open the HTML report when done")
	fs.BoolVar(&o.noOpen, "no-open", false, "never open a browser")
	fs.BoolVar(&o.stop, "stop", false, "stop the report server for --out")
	fs.Var(&o.with, "with", "run an external reviewer; repeat or comma-separate for several (claude, cursor, none)")
	fs.StringVar(&o.reportURL, "report-url", "", "with post: link to the full report in the review body (e.g. a CI artifact URL)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with post: print the review payload as JSON instead of posting")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
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
	case "post":
		return cmdPost(o)
	case "open":
		return announce(o.out, o.port, true)
	case "serve":
		if o.stop {
			return report.Stop(o.out)
		}
		return report.Serve(o.out, o.port)
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", cmd, usage)
	}
}

// target is the subject the flags name, independent of any repository.
func (o opts) target() target.Options {
	return target.Options{PR: o.pr, Branch: o.branch, Commit: o.commit,
		Range: o.revRange, Base: o.base}
}

func (o opts) toRun(dir string) run.Options {
	return run.Options{Dir: dir, Base: o.base, Upstream: o.upstream,
		MigDir: o.migDir, PR: o.pr, Branch: o.branch, Commit: o.commit,
		Range: o.revRange, Out: o.out}
}

func cmdRun(o opts) error {
	res, err := execute(o)
	if err != nil {
		return err
	}
	if err := withReviewer(o, res); err != nil {
		return err
	}
	if err := write(o, res, nil); err != nil {
		return err
	}
	if o.format == "json" {
		return emitJSON(res.Report)
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence, res.Packet, nil))
	return announce(o.out, o.port, o.open && !o.noOpen)
}

// cmdReview emits the packet. Redline stops here: what it hands over is facts,
// and the judgment is the agent's. Nothing in this path talks to a model unless
// asked to, which is what `--with` does.
func cmdReview(o opts) error {
	res, err := execute(o)
	if err != nil {
		return err
	}
	if err := withReviewer(o, res); err != nil {
		return err
	}
	if err := write(o, res, nil); err != nil {
		return err
	}
	_ = announce(o.out, o.port, o.open && !o.noOpen)
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
	// Ingest merges into the packet the agent judged; it does not re-observe.
	// A target flag that names something else is a mistake, not a request —
	// silently merging a review of #456 into the session for #123 would put
	// findings against the wrong change under a reviewer's name.
	if o.target().Requested() {
		if err := res.Target.Matches(o.target()); err != nil {
			return fmt.Errorf("ingest: %w (run `redline review` for that target first)", err)
		}
	}
	packet.Apply(&res.Report, rev)
	// Screenshots from an earlier ingest are carried forward only while their
	// files still exist; a cleaned temp directory must not fail this run.
	if res.Review != nil {
		res.Review.Screenshots = report.PruneMissingShots(res.Review.Screenshots)
	}
	res.Review = packet.Merge(res.Review, rev)
	if err := write(o, res, res.Review); err != nil {
		return err
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence, res.Packet, res.Review))
	return announce(o.out, o.port, !o.noOpen)
}

// announce prints where the report is and, when asked, opens it. Serving and
// browsing are best effort: the report is already on disk by the time this
// runs, and exiting non-zero because a port was taken would tell the agent
// driving the session that a completed review failed.
//
// Only report.ErrNoReport is fatal, and it is matched explicitly. Inferring
// it from an empty URL is what broke this once: failing to start a server
// also yields no URL, so a review that had written its report exited 1 and
// printed no path to it at all.
func announce(out string, port int, browse bool) error {
	url, err := report.OpenOn(out, browse, port)
	if errors.Is(err, report.ErrNoReport) {
		return err
	}
	if url == "" {
		// Nothing is serving it, but it is on disk. Say where.
		url = filepath.Join(out, "report.html")
	}
	fmt.Fprintf(os.Stderr, "Report: %s\n", url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "redline: %v\n", err)
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
	md := report.Markdown(&res.Report, res.Renders, res.Evidence, res.Packet, rev)
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	var shots []report.Screenshot
	if rev != nil {
		var err error
		shots, err = report.MaterializeShots(dir, rev.Screenshots)
		if err != nil {
			return err
		}
	}
	html, err := report.HTML(report.HTMLInput{
		Report: &res.Report, Packet: res.Packet, Review: rev,
		Renders: res.Renders, Evidence: res.Evidence, Screenshots: shots,
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
