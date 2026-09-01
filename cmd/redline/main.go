// Command redline reviews a change: the working tree, a commit, a commit
// range, a branch, or a GitHub pull request.
//
// `redline run` observes the change and reports what it can establish
// deterministically: findings.json, report.md, and report.html. Redline
// measures; it never invokes a model, and the judgment of the change stays
// with whoever is reviewing it.
//
// `redline post` is the one command that writes to GitHub: it submits the
// session's observed findings as one pull request review. It is always
// explicit — `run` stays read-only.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
	"github.com/ccason/redline/internal/target"
)

const usage = `redline — observe a change and report the evidence

usage:
  redline run     [flags]   observe the change and report (the entry point)
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
  --open            open the HTML report when done
  --no-open         never open a browser
  --port N          loopback port for open/serve (default 8765; the next
                    free port is used if it is taken)
  --report-url URL  with post: link to the full report in the review body
  --profile PATH    with post: YAML that stamps pass/fail markers a merge
                    gate can read (error and warning fail unless the file
                    says otherwise). Without it, post still comments and
                    never approves.
  --dry-run         with post: print the review payload instead of posting
  --stop            with serve: stop the server for --out
`

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redline:", err)
		os.Exit(1)
	}
}

type opts struct {
	base, upstream, migDir, format, out, pr, branch, commit, revRange string
	reportURL, profile                                                string
	open, noOpen, stop, dryRun                                        bool
	port                                                              int
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
	fs.StringVar(&o.reportURL, "report-url", "", "with post: link to the full report in the review body (e.g. a CI artifact URL)")
	fs.StringVar(&o.profile, "profile", "", "with post: YAML profile for merge-gate pass/fail markers")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with post: print the review payload as JSON instead of posting")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch cmd {
	case "run":
		return cmdRun(o)
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
	if err := write(o, res); err != nil {
		return err
	}
	if o.format == "json" {
		return emitJSON(res.Report)
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence, res.Change))
	return announce(o.out, o.port, o.open && !o.noOpen)
}

// announce prints where the report is and, when asked, opens it. Serving and
// browsing are best effort: the report is already on disk by the time this
// runs, and exiting non-zero because a port was taken would tell the agent
// driving the session that a completed review failed.
//
// Only report.ErrNoReport is fatal, and it is matched explicitly. Inferring
// it from an empty URL is what broke this once: failing to start a server
// also yields no URL, so a run that had written its report exited 1 and
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

// write persists the run: findings.json, report.md, report.html, and any
// captured artifacts. comments.json is never written here — it is the
// reviewer's own state, and merging the two would clobber their decisions on
// every re-run.
func write(o opts, res *run.Result) error {
	dir := o.out
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "findings.json"), res.Report); err != nil {
		return err
	}
	if err := run.SaveSession(dir, res); err != nil {
		return err
	}
	md := report.Markdown(&res.Report, res.Renders, res.Evidence, res.Change)
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	html, err := report.HTML(report.HTMLInput{
		Report: &res.Report, Change: res.Change,
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
