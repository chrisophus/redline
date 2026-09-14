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
	"os"
	"path/filepath"

	"github.com/chrisophus/redline/internal/pane"
	"github.com/chrisophus/redline/internal/report"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

const usage = `redline — observe a change and report the evidence

usage:
  redline run     [flags]   observe the change and report (the entry point)
  redline review  [flags]   review the last run with a model and merge the result
  redline learnings [flags] draft review rules from what people said about
                            earlier findings on this pull request
  redline post    [flags]   post the session's findings as one PR review (--pr)
  redline postmortem [flags] read back what the last review did: what it
                             proposed, what the lookups found, what was ruled
  redline open    [flags]   serve and open the last report (--file opens it from disk, no server)
  redline serve   [flags]   serve .redline over http (blocks; --stop ends it)
  redline gc      [flags]   remove this repo's cached review worktrees (~/.redline/worktrees)
  redline version           print the version and build info

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
  --format FMT      report|json  (default report; with postmortem, json is
                    the whole trace as it was written)
  --out DIR         evidence directory (default .redline)
  --prepare         run harness produce steps from .redline.yml before observe
  --allow-missing-coverage  do not fail when a configured coverage profile is absent
  --open            open the HTML report when done
  --no-open         never open a browser
  --file            open (or print) the report as a file:// path, no server
  --port N          loopback port for open/serve (default 8765; the next
                    free port is used if it is taken)
  --api NAME        with review: anthropic (default) or openai. openai is any
                    endpoint speaking the OpenAI chat completions protocol,
                    a proxy included; one shot only.
  --base-url URL    with review: endpoint to send the call to, for a proxy
                    (default: the vendor's public endpoint; also read from
                    ANTHROPIC_BASE_URL or OPENAI_BASE_URL)
  --api-user NAME   with review --api openai: name the caller to a proxy that
                    wants one, sent as "Bearer user=NAME&key=KEY" (also read
                    from OPENAI_USER)
  --model NAME      with review: model to review with, and to check the
                    findings with (default claude-sonnet-5, or gpt-5 with
                    --api openai)
  --effort LEVEL    with review: low|medium|high|xhigh|max, for the review and
                    the checking alike (default: the model's for the review,
                    low for the checking)
  --scout-model NAME
                    with review: model to check the findings with, when it
                    should differ from --model
  --scout-effort LEVEL
                    with review: effort for the checking, when it should
                    differ from --effort
  --mode MODE       with review: oneshot (default) sends the context Redline
                    chose; explore sends a catalogue and lets the reviewer
                    fetch what it wants, costing more by design. In explore
                    mode --max-cost is a governor, not a tripwire.
  --max-turns N     with review --mode explore: turn limit (default 5)
  --samples N       with review: take N independent reviews and union them
                    (default 1). Samples do not overlap, so recall rises with
                    N and cost rises with it too; the calls go out together,
                    so wall time does not.
  --verify          with review: look up what each finding said would settle
                    it, then rule on every finding with the answers in hand.
                    Only findings the ruling keeps are posted; the rest stay
                    on the report with the reason. Off by default as of the
                    2026-09-13 measurements: it ran by default for most of
                    this tool's life and no eval arm has ever scored it, so
                    the scout's lookup turns and the ruling that reads them
                    were unmeasured cost on every review. The one trace that
                    exists points the wrong way - on this repository's PR #46
                    nine of the ten findings that never reached the reader
                    came back unverifiable from the ruling, six of those on
                    questions the review had certified itself.
  --cache           with review: mark the prompt the review and the ruling
                    share for the prompt cache, so the second call reads it
                    back instead of paying for it again. On by default;
                    --no-cache sends both at full input rate. Ignored where
                    the write could not be read: --api openai, --samples
                    above one, and the batch tier.
  --cache-ttl D     with review: how long the cached prefix lives, 5m
                    (default) or 1h. The hour costs 2x base input to write
                    against the five minutes' 1.25x, and is worth it only
                    when the gap between the two calls runs past five
                    minutes.
  --synopsis        with review: describe the change in its own call, then
                    judge it in a second one that writes only the comments
                    and the verdicts. The two share a prefix, so the second
                    reads what the first cached. Off by default;
                    --no-synopsis is the explicit off. What it buys is a
                    walkthrough that covers every shown file and an output
                    cap the findings no longer share with fifty file
                    summaries.
  --brief           with review: one call under the forty-line short prompt,
                    carried by the same tool grammar every other stage uses.
                    Measured on 2026-09-14 against the long prompt, it
                    returned a placeholder in place of a review on 22 of 168
                    calls, where the long prompt did on none of 42, and left
                    about twice the unlabelled comments. It cannot be combined
                    with --pipeline staged, --synopsis or --mode explore, each
                    defined by a tool contract the short prompt does not
                    describe.
  --no-brief        with review: the long prompt, which is the default. Kept
                    so scripts that pass it still run.
  --pipeline SHAPE  with review: oneshot (default) is one call that judges
                    the whole change. staged describes it first, splits the
                    shown files into cohorts, and reviews each cohort in its
                    own call over the same cached prefix. What it is aimed at
                    is measured: a reviewer with fifty files in front of it
                    spends its finding-count on the first few. Staged implies
                    the describing call, so --synopsis is not also needed.
  --cohorts N       with review: upper bound on parallel cohort reviews
                    (default 6). Stage one draws fewer when the change has
                    fewer groups in it; the tripwire prices the bound.
  --min-cohort-files N
                    with review: below this many shown files a staged run is
                    one cohort (default 3).
  --no-cross-summaries
                    with review: each cohort is told nothing about its
                    neighbours. Cheaper, and gives up the correlations a
                    cohort call raises about a change it can see but was not
                    given.
  --ceiling N       with review: token ceiling for the whole request
                    (default 250000). A tail bound, not a per-review budget:
                    a change whose diff and findings alone exceed it is
                    refused, not reviewed with the context dropped.
  --max-tokens N    with review: cap on the response (default 64000)
  --max-cost USD    with review: refuse to send a request estimated above this
                    (default 2.00). A tripwire, not a governor.
  --stats           with review: print the cost distribution of the reviews
                    recorded in --out and exit. The target is an average, so
                    this is the number to read, not any single run.
  --debug           with review: log every model request, its stop reason and
                    token counts, the body the parser was handed, and each
                    scout tool call to stderr, and write the full requests and
                    responses under --out/debug. REDLINE_DEBUG does the same.
  --report-url URL  with post: link to the full report in the review body
  --profile PATH    with post: YAML that stamps pass/fail markers a merge
                    gate can read (error and warning fail unless the file
                    says otherwise). Without it, post still comments and
                    never approves.
  --dry-run         with post: print the review payload instead of posting;
                    with review: print the assembled prompt and its estimated
                    cost, and call nothing
  --stop            with serve: stop the server for --out
  --older-than D    with gc: only remove cached worktrees older than D (e.g. 168h)
`

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
	stats, verify, noVerify, debug, cache, noCache                    bool
	synopsis, noSynopsis                                              bool
	brief, noBrief                                                    bool
	port, ceiling, maxTokens, maxTurns, samples                       int
	cohorts, minCohortFiles                                           int
	maxCost                                                           float64
	cacheTTL, pipeline                                                string
	crossSummaries, noCrossSummaries                                  bool
}

func runMain(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	if args[0] == "version" || args[0] == "--version" || args[0] == "-v" {
		fmt.Printf("redline %s (commit %s, built %s)\n", version, commit, date)
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
	fs.BoolVar(&o.prepare, "prepare", false, "run harness produce steps from .redline.yml before observe")
	fs.BoolVar(&o.allowMissingCoverage, "allow-missing-coverage", false, "do not fail when a configured coverage profile is missing or stale")
	fs.BoolVar(&o.open, "open", false, "open the HTML report when done")
	fs.BoolVar(&o.noOpen, "no-open", false, "never open a browser")
	fs.BoolVar(&o.stop, "stop", false, "stop the report server for --out")
	fs.BoolVar(&o.stats, "stats", false, "with review: print the recorded cost distribution and exit")
	fs.BoolVar(&o.file, "file", false, "open or print the report as a file:// path, no server")
	fs.StringVar(&o.reportURL, "report-url", "", "with post: link to the full report in the review body (e.g. a CI artifact URL)")
	fs.StringVar(&o.profile, "profile", "", "with post: YAML profile for merge-gate pass/fail markers")
	fs.BoolVar(&o.dryRun, "dry-run", false, "with post: print the review payload as JSON instead of posting")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
	fs.StringVar(&o.api, "api", "", "with review: anthropic or openai")
	fs.StringVar(&o.baseURL, "base-url", "", "with review: endpoint to send the call to, for a proxy")
	fs.StringVar(&o.apiUser, "api-user", "", "with review --api openai: caller name for a proxy that wants one beside the key")
	fs.StringVar(&o.model, "model", "", "with review: model to review and check the findings with")
	fs.StringVar(&o.effort, "effort", "", "with review: low|medium|high|xhigh|max, for the review and the checking")
	fs.StringVar(&o.scoutModel, "scout-model", "", "with review: model to check the findings with, when it should differ from --model")
	fs.StringVar(&o.scoutEffort, "scout-effort", "", "with review: effort for the checking, when it should differ from --effort")
	fs.StringVar(&o.mode, "mode", "", "with review: oneshot or explore")
	fs.IntVar(&o.maxTurns, "max-turns", 0, "with review --mode explore: turn limit")
	fs.IntVar(&o.samples, "samples", 0, "with review: independent reviews to union")
	fs.BoolVar(&o.verify, "verify", false, "with review: check each finding against the repository before posting it")
	fs.BoolVar(&o.noVerify, "no-verify", false, "with review: skip the checking pass")
	fs.BoolVar(&o.cache, "cache", false, "with review: mark the shared prefix for the prompt cache (on by default)")
	fs.BoolVar(&o.noCache, "no-cache", false, "with review: send every call at full input rate")
	fs.BoolVar(&o.synopsis, "synopsis", false, "with review: describe the change in its own call before judging it")
	fs.BoolVar(&o.noSynopsis, "no-synopsis", false, "with review: one call writes the walkthrough and the findings together")
	fs.BoolVar(&o.brief, "brief", false, "with review: one call under the short prompt")
	fs.BoolVar(&o.noBrief, "no-brief", false, "with review: the long prompt, which is already the default")
	fs.StringVar(&o.cacheTTL, "cache-ttl", "", "with review: how long the cached prefix lives, 5m or 1h")
	fs.StringVar(&o.pipeline, "pipeline", "", "with review: oneshot, or staged to split the change into cohorts and review each")
	fs.IntVar(&o.cohorts, "cohorts", 0, "with review: upper bound on parallel cohort reviews under --pipeline staged (default 6)")
	fs.IntVar(&o.minCohortFiles, "min-cohort-files", 0, "with review: below this many shown files a staged run is one cohort (default 3)")
	fs.BoolVar(&o.crossSummaries, "cross-summaries", false, "with review: give each cohort the other cohorts' summaries (on by default)")
	fs.BoolVar(&o.noCrossSummaries, "no-cross-summaries", false, "with review: each cohort reviews its files knowing nothing of the others")
	fs.BoolVar(&o.debug, "debug", false, "with review: log each model request, response, and scout tool call to stderr")
	fs.IntVar(&o.ceiling, "ceiling", 0, "with review: token ceiling for the whole request")
	fs.IntVar(&o.maxTokens, "max-tokens", 0, "with review: cap on the response")
	fs.Float64Var(&o.maxCost, "max-cost", 0, "with review: refuse a request estimated above this many dollars")
	fs.StringVar(&o.olderThan, "older-than", "", "with gc: only remove cached worktrees older than this duration (e.g. 168h)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch cmd {
	case "run":
		return cmdRun(o)
	case "review":
		return cmdReview(o)
	case "post":
		return cmdPost(o)
	case "postmortem":
		return cmdPostmortem(o)
	case "learnings":
		return cmdLearnings(o)
	case "open":
		if o.file {
			return openFile(o.out, true)
		}
		return announce(o.out, o.port, true)
	case "serve":
		if o.stop {
			return report.Stop(o.out)
		}
		return report.Serve(o.out, o.port)
	case "gc":
		return cmdGC(o)
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
		Range: o.revRange, Out: o.out, AllowMissingCoverage: o.allowMissingCoverage,
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
