// Command redline reviews a change: the working tree, a commit, a commit
// range, a branch, or a GitHub pull request.
//
// `redline run` observes the change and reports what it can establish
// deterministically: findings.json, report.md, and report.html. The
// reviewer's own comments and verdicts — human or agent, via review.json —
// merge into the same report, each finding marked with its source. `run`
// invokes no model, by any route.
//
// `redline review` is the one command that does. It reviews the session
// `run` already wrote, in a single call with no tools, and merges the result
// through the same review.json every other reviewer writes. Keeping it a
// separate command is the whole boundary: observing costs nothing and needs
// no credentials, judging costs money, and a repository without an API key
// gets the entire report minus the judgment.
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
	"io"
	"os"
	"path/filepath"

	"github.com/chrisophus/redline/internal/pane"
	"github.com/chrisophus/redline/internal/report"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

// Build information, injected by the linker at release time (see
// .goreleaser.yaml). A plain `go build` leaves these at their defaults, so
// `redline version` reads "dev" off a local build and the tag off a release.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redline:", err)
		os.Exit(1)
	}
}

type opts struct {
	base, upstream, migDir, format, out, pr, branch, commit, revRange string
	reportURL, profile, olderThan                                     string
	model, effort, mode, api, baseURL, apiUser                        string
	scoutModel, scoutEffort                                           string
	open, noOpen, stop, dryRun, file, prepare, allowMissingCoverage   bool
	noLint                                                            bool
	stats, verify, noVerify, debug, cache, noCache                    bool
	synopsis, noSynopsis                                              bool
	brief, noBrief                                                    bool
	port, ceiling, maxTokens, maxTurns, samples                       int
	cohorts, minCohortFiles                                           int
	maxCost                                                           float64
	cacheTTL, pipeline                                                string
	crossSummaries, noCrossSummaries                                  bool
	// observe is review's --run. session is --session; root is the --out the
	// caller gave, and sessionKey the session resolveSession chose under it.
	observe                   bool
	thinking                  bool
	session, root, sessionKey string
}

func runMain(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		if len(args) > 1 {
			c, ok := commandNamed(args[1])
			if !ok {
				return fmt.Errorf("unknown subcommand %q\n\n%s", args[1], usage())
			}
			fmt.Print(commandHelp(c))
			return nil
		}
		fmt.Print(usage())
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Printf("redline %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}
	c, ok := commandNamed(args[0])
	if !ok {
		return fmt.Errorf("unknown subcommand %q\n\n%s", args[0], usage())
	}
	fs := flag.NewFlagSet("redline "+c.name, flag.ContinueOnError)
	// The flag package prints its own terse listing on a parse error; the
	// command's help is the listing, so that output is discarded and the error
	// points at it.
	fs.SetOutput(io.Discard)
	var o opts
	c.flags(fs, &o)
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(commandHelp(c))
			return nil
		}
		return fmt.Errorf("%w (see `redline help %s`)", err, c.name)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := resolveSession(c, &o, set); err != nil {
		return err
	}
	return c.run(o)
}

func isHelpArg(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}

func cmdOpen(o opts) error {
	if o.file {
		return openFile(o.out, true)
	}
	return announce(o.out, o.port, true)
}

func cmdServe(o opts) error {
	if o.stop {
		return report.Stop(o.out)
	}
	return report.Serve(o.out, o.port)
}

// target is the subject the flags name, independent of any repository.
func (o opts) target() target.Options {
	return target.Options{PR: o.pr, Branch: o.branch, Commit: o.commit,
		Range: o.revRange, Base: o.base}
}

func (o opts) toRun(dir string) run.Options {
	return run.Options{Dir: dir, Base: o.base, Upstream: o.upstream,
		MigDir: o.migDir, PR: o.pr, Branch: o.branch, Commit: o.commit,
		Range: o.revRange, Out: o.out, AllowMissingCoverage: o.allowMissingCoverage,
		SkipLint: o.noLint,
		// Which tree was observed decides what the harness could see, so it
		// is said out loud rather than left to be inferred from a coverage
		// number that came back missing.
		Progress: func(msg string) { fmt.Fprintln(os.Stderr, "redline:", msg) },
	}
}

func cmdRun(o opts) error {
	res, runErr := execute(o)
	if res == nil {
		return runErr
	}
	if err := write(o, res); err != nil {
		return err
	}
	recordSession(o)
	if o.format == "json" {
		if err := emitJSON(res.Report); err != nil {
			return err
		}
		return runErr
	}
	fmt.Print(report.Markdown(&res.Report, res.Renders, res.Evidence, res.Change))
	if o.file {
		if err := openFile(o.out, o.open && !o.noOpen); err != nil {
			return err
		}
		return runErr
	}
	if err := announce(o.out, o.port, o.open && !o.noOpen); err != nil {
		return err
	}
	return runErr
}

// openFile points at report.html on disk, no server. The page is
// self-contained, so a browser renders it straight from a file:// path; only
// the click-to-comment feature, which needs a stable origin for
// localStorage, wants the server. This is the answer to a report that lands
// in .redline, a directory the file picker hides: the path is handed to the
// opener directly, so no picker is involved. With browse, it opens; without,
// it prints the file:// URL to click.
func openFile(out string, browse bool) error {
	abs, err := filepath.Abs(filepath.Join(out, "report.html"))
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("no report at %s (run `redline run` first)", abs)
	}
	fmt.Fprintf(os.Stderr, "Report: file://%s\n", abs)
	if browse {
		return report.Open(abs)
	}
	return nil
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
	if o.prepare {
		if _, err := run.Prepare(o.toRun(cwd)); err != nil {
			return nil, err
		}
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
		Renders: res.Renders, Evidence: res.Evidence, LineCoverage: res.LineCoverage,
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
