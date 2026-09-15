package main

import (
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/chrisophus/redline/internal/report"
)

// command is one subcommand: the flags it accepts and the help that lists
// them. Each command registers only the flags it reads, so a flag meant for
// another command is refused at parse time instead of being accepted and
// ignored, which is how `redline review --pr 7` used to review whatever the
// last run observed.
type command struct {
	name    string
	summary string
	flags   func(fs *flag.FlagSet, o *opts)
	help    string
	run     func(o opts) error
}

// commands is the order `redline help` lists them in.
var commands = []command{
	{name: "run", summary: "observe the change and report (the entry point)",
		flags: runFlags, help: runHelp, run: cmdRun},
	{name: "review", summary: "review the last run with a model and merge the result",
		flags: reviewFlags, help: reviewHelp, run: cmdReview},
	{name: "learnings", summary: "draft review rules from what people said about\n              earlier findings on this pull request",
		flags: outFlag, help: learningsHelp, run: cmdLearnings},
	{name: "post", summary: "post the session's findings as one PR review",
		flags: postFlags, help: postHelp, run: cmdPost},
	{name: "postmortem", summary: "read back what the last review did: what it\n              proposed, what the lookups found, what was ruled",
		flags: postmortemFlags, help: postmortemHelp, run: cmdPostmortem},
	{name: "open", summary: "serve and open the last report",
		flags: openFlags, help: openHelp, run: cmdOpen},
	{name: "serve", summary: "serve .redline over http (blocks; --stop ends it)",
		flags: serveFlags, help: serveHelp, run: cmdServe},
	{name: "gc", summary: "remove this repo's cached review worktrees (~/.redline/worktrees)",
		flags: gcFlags, help: gcHelp, run: cmdGC},
}

func commandNamed(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// globalFlags are the flags listed once in `redline help` rather than under
// each command. --out is every command's but gc's, which works on
// ~/.redline/worktrees and has no session to find.
var globalFlags = []string{"out"}

func usage() string {
	var b strings.Builder
	b.WriteString(`redline — observe a change and report the evidence

usage:
  redline <command> [flags]
  redline help <command>    the flags that command takes

commands:
`)
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	b.WriteString(`  version     print the version and build info

global flags:
  --out DIR         evidence directory (default .redline). Every command but
                    gc and version reads or writes its session there.
  -h, --help        print this; after a command, that command's flags
`)
	return b.String()
}

func commandHelp(c command) string {
	for _, name := range registeredFlags(c) {
		if slices.Contains(globalFlags, name) {
			return c.help + "\nGlobal flags such as --out are listed by `redline help`.\n"
		}
	}
	return c.help
}

func registeredFlags(c command) []string {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	var o opts
	c.flags(fs, &o)
	var out []string
	fs.VisitAll(func(f *flag.Flag) { out = append(out, f.Name) })
	return out
}

func outFlag(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.out, "out", ".redline", "evidence directory")
}

func targetFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.pr, "pr", "", "GitHub pull request number or URL")
	fs.StringVar(&o.branch, "branch", "", "branch to review")
	fs.StringVar(&o.commit, "commit", "", "commit to review against its parent")
	fs.StringVar(&o.revRange, "range", "", "commit range A..B")
}

func browseFlags(fs *flag.FlagSet, o *opts) {
	fs.BoolVar(&o.open, "open", false, "open the HTML report when done")
	fs.BoolVar(&o.noOpen, "no-open", false, "never open a browser")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
}

func runFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	targetFlags(fs, o)
	browseFlags(fs, o)
	fs.StringVar(&o.base, "base", "", "base revision")
	fs.StringVar(&o.upstream, "upstream", "", "upstream branch for version-collision checks")
	fs.StringVar(&o.migDir, "migrations", "", "migrations directory")
	fs.StringVar(&o.format, "format", "report", "report|json")
	fs.BoolVar(&o.prepare, "prepare", false, "run harness produce steps from .redline.yml before observe")
	fs.BoolVar(&o.allowMissingCoverage, "allow-missing-coverage", false, "do not fail when a configured coverage profile is missing or stale")
	fs.BoolVar(&o.noLint, "no-lint", false, "skip lint delta, suppression, and configuration checks")
	fs.BoolVar(&o.file, "file", false, "print the report as a file:// path, no server")
}

func reviewFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	browseFlags(fs, o)
	fs.BoolVar(&o.stats, "stats", false, "print the recorded cost distribution and exit")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the assembled prompt and its estimated cost, and call nothing")
	fs.StringVar(&o.api, "api", "", "anthropic or openai")
	fs.StringVar(&o.baseURL, "base-url", "", "endpoint to send the call to, for a proxy")
	fs.StringVar(&o.apiUser, "api-user", "", "with --api openai: caller name for a proxy that wants one beside the key")
	fs.StringVar(&o.model, "model", "", "model to review and check the findings with")
	fs.StringVar(&o.effort, "effort", "", "low|medium|high|xhigh|max, for the review and the checking")
	fs.StringVar(&o.scoutModel, "scout-model", "", "model to check the findings with, when it should differ from --model")
	fs.StringVar(&o.scoutEffort, "scout-effort", "", "effort for the checking, when it should differ from --effort")
	fs.StringVar(&o.mode, "mode", "", "oneshot or explore")
	fs.IntVar(&o.maxTurns, "max-turns", 0, "with --mode explore: turn limit")
	fs.IntVar(&o.samples, "samples", 0, "independent reviews to union")
	fs.BoolVar(&o.verify, "verify", false, "check each finding against the repository before posting it")
	fs.BoolVar(&o.noVerify, "no-verify", false, "skip the checking pass")
	fs.BoolVar(&o.cache, "cache", false, "mark the shared prefix for the prompt cache (on by default)")
	fs.BoolVar(&o.noCache, "no-cache", false, "send every call at full input rate")
	fs.StringVar(&o.cacheTTL, "cache-ttl", "", "how long the cached prefix lives, 5m or 1h")
	fs.BoolVar(&o.synopsis, "synopsis", false, "describe the change in its own call before judging it")
	fs.BoolVar(&o.noSynopsis, "no-synopsis", false, "one call writes the walkthrough and the findings together")
	fs.BoolVar(&o.brief, "brief", false, "one call under the short prompt")
	fs.BoolVar(&o.noBrief, "no-brief", false, "the long prompt, which is already the default")
	fs.StringVar(&o.pipeline, "pipeline", "", "oneshot, staged to split the change into cohorts and review each, or stepwise to describe from the diff before seeing the rest")
	fs.IntVar(&o.cohorts, "cohorts", 0, "upper bound on parallel cohort reviews under --pipeline staged (default 6)")
	fs.IntVar(&o.minCohortFiles, "min-cohort-files", 0, "below this many shown files a staged run is one cohort (default 3)")
	fs.BoolVar(&o.crossSummaries, "cross-summaries", false, "give each cohort the other cohorts' summaries (on by default)")
	fs.BoolVar(&o.noCrossSummaries, "no-cross-summaries", false, "each cohort reviews its files knowing nothing of the others")
	fs.BoolVar(&o.debug, "debug", false, "log each model request, response, and scout tool call to stderr")
	fs.IntVar(&o.ceiling, "ceiling", 0, "token ceiling for the whole request")
	fs.IntVar(&o.maxTokens, "max-tokens", 0, "cap on the response")
	fs.Float64Var(&o.maxCost, "max-cost", 0, "refuse a request estimated above this many dollars")
}

// postFlags keeps --branch, --commit and --range so post can say why they do
// not apply, rather than leaving the reader with an undefined-flag error.
func postFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	targetFlags(fs, o)
	fs.StringVar(&o.reportURL, "report-url", "", "link to the full report in the review body (e.g. a CI artifact URL)")
	fs.StringVar(&o.profile, "profile", "", "YAML profile for merge-gate pass/fail markers")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the review payload as JSON instead of posting")
}

func postmortemFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	fs.StringVar(&o.format, "format", "report", "report|json")
}

func openFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	fs.BoolVar(&o.file, "file", false, "open the report as a file:// path, no server")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
}

func serveFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
	fs.BoolVar(&o.stop, "stop", false, "stop the report server for --out")
}

func gcFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.olderThan, "older-than", "", "only remove cached worktrees older than this duration (e.g. 168h)")
}

const runHelp = `redline run — observe the change and report

usage:
  redline run [flags]

Writes findings.json, report.md and report.html under --out and prints the
markdown report. Invokes no model.

target (pass only one):
  (default)         working tree, uncommitted work included
  --commit REF      that commit against its parent (HEAD for the latest)
  --range A..B      commits reachable from B but not A (B defaults to HEAD)
  --branch REF      a branch's tip against --base
  --pr N|URL        a GitHub pull request (uses gh; read-only)

flags:
  --base REF        base revision (default: commit parent, range start, PR
                    base, else origin/main)
  --upstream REF    branch new migrations must not collide with
  --migrations DIR  restrict migration checks to one directory
  --format FMT      report|json (default report)
  --prepare         run harness produce steps from .redline.yml before observe
  --allow-missing-coverage
                    do not fail when a configured coverage profile is absent
  --no-lint         skip lint delta, suppression, and configuration checks
  --open            open the HTML report when done
  --no-open         never open a browser
  --file            print the report as a file:// path, no server
  --port N          loopback port for the report server (default 8765; the
                    next free port is used if it is taken)
`

const reviewHelp = `redline review — review the last run with a model and merge the result

usage:
  redline run [target flags]
  redline review [flags]

Reviews the session the last ` + "`redline run`" + ` wrote under --out. It takes no
target flags of its own: name the pull request, branch, commit or range on
run.

where the call goes:
  --api NAME        anthropic (default) or openai. openai is any endpoint
                    speaking the OpenAI chat completions protocol, a proxy
                    included; one shot only.
  --base-url URL    endpoint to send the call to, for a proxy (default: the
                    vendor's public endpoint; also read from
                    ANTHROPIC_BASE_URL or OPENAI_BASE_URL)
  --api-user NAME   with --api openai: name the caller to a proxy that wants
                    one, sent as "Bearer user=NAME&key=KEY" (also read from
                    OPENAI_USER)

model:
  --model NAME      model to review with, and to check the findings with
                    (default claude-sonnet-5, or gpt-5 with --api openai)
  --effort LEVEL    low|medium|high|xhigh|max, for the review and the checking
                    alike (default: the model's for the review, low for the
                    checking)
  --scout-model NAME
                    model to check the findings with, when it should differ
                    from --model
  --scout-effort LEVEL
                    effort for the checking, when it should differ from
                    --effort

shape:
  --mode MODE       oneshot (default) sends the context Redline chose;
                    explore sends a catalogue and lets the reviewer fetch
                    what it wants, costing more by design. In explore mode
                    --max-cost is a governor, not a tripwire.
  --max-turns N     with --mode explore: turn limit (default 5)
  --samples N       take N independent reviews and union them (default 1).
                    Samples do not overlap, so recall rises with N and cost
                    rises with it too; the calls go out together, so wall
                    time does not.
  --verify          look up what each finding said would settle it, then rule
                    on every finding with the answers in hand. Only findings
                    the ruling keeps are posted; the rest stay on the report
                    with the reason. Off by default as of the 2026-09-13
                    measurements: it ran by default for most of this tool's
                    life and no eval arm has ever scored it, so the scout's
                    lookup turns and the ruling that reads them were
                    unmeasured cost on every review. The one trace that
                    exists points the wrong way - on this repository's PR #46
                    nine of the ten findings that never reached the reader
                    came back unverifiable from the ruling, six of those on
                    questions the review had certified itself.
  --no-verify       skip the checking pass, which is already the default
  --synopsis        describe the change in its own call, then judge it in a
                    second one that writes only the comments and the
                    verdicts. The two share a prefix, so the second reads
                    what the first cached. Off by default; --no-synopsis is
                    the explicit off. What it buys is a walkthrough that
                    covers every shown file and an output cap the findings no
                    longer share with fifty file summaries.
  --no-synopsis     one call writes the walkthrough and the findings together
  --brief           one call under the forty-line short prompt, carried by
                    the same tool grammar every other stage uses. Measured on
                    2026-09-14 against the long prompt, it returned a
                    placeholder in place of a review on 22 of 168 calls,
                    where the long prompt did on none of 42, and left about
                    twice the unlabelled comments. It cannot be combined with
                    --pipeline staged, --synopsis or --mode explore, each
                    defined by a tool contract the short prompt does not
                    describe.
  --no-brief        the long prompt, which is the default. Kept so scripts
                    that pass it still run.
  --pipeline SHAPE  oneshot (default) is one call that judges the whole
                    change. staged describes it first, splits the shown files
                    into cohorts, and reviews each cohort in its own call
                    over the same cached prefix. What it is aimed at is
                    measured: a reviewer with fifty files in front of it
                    spends its finding-count on the first few. Staged implies
                    the describing call, so --synopsis is not also needed.
                    stepwise is one conversation in two turns: the first sees
                    the change and its diff and writes the walkthrough, the
                    second gets the findings, coverage and context and writes
                    the comments. The second resends the first, so it reads
                    that from the cache. Anthropic wire only, and it cannot
                    be combined with --brief, --synopsis or --mode explore.
  --cohorts N       upper bound on parallel cohort reviews (default 6). Stage
                    one draws fewer when the change has fewer groups in it;
                    the tripwire prices the bound.
  --min-cohort-files N
                    below this many shown files a staged run is one cohort
                    (default 3).
  --cross-summaries give each cohort the other cohorts' summaries, which is
                    the default
  --no-cross-summaries
                    each cohort is told nothing about its neighbours.
                    Cheaper, and gives up the correlations a cohort call
                    raises about a change it can see but was not given.

cost and caching:
  --cache           mark the prompt the review and the ruling share for the
                    prompt cache, so the second call reads it back instead of
                    paying for it again. On by default; --no-cache sends both
                    at full input rate. Ignored where the write could not be
                    read: --api openai, --samples above one, and the batch
                    tier.
  --no-cache        send every call at full input rate
  --cache-ttl D     how long the cached prefix lives, 5m (default) or 1h. The
                    hour costs 2x base input to write against the five
                    minutes' 1.25x, and is worth it only when the gap between
                    the two calls runs past five minutes.
  --ceiling N       token ceiling for the whole request (default 250000). A
                    tail bound, not a per-review budget: a change whose diff
                    and findings alone exceed it is refused, not reviewed
                    with the context dropped.
  --max-tokens N    cap on the response (default 64000)
  --max-cost USD    refuse to send a request estimated above this (default
                    2.00). A tripwire, not a governor.

output:
  --dry-run         print the assembled prompt and its estimated cost, and
                    call nothing
  --stats           print the cost distribution of the reviews recorded in
                    --out and exit. The target is an average, so this is the
                    number to read, not any single run.
  --debug           log every model request, its stop reason and token
                    counts, the body the parser was handed, and each scout
                    tool call to stderr, and write the full requests and
                    responses under --out/debug. REDLINE_DEBUG does the same.
  --open            open the HTML report when done
  --no-open         never open a browser
  --port N          loopback port for the report server (default 8765)
`

const learningsHelp = `redline learnings — draft review rules from earlier review threads

usage:
  redline learnings

Reads what people said about earlier findings on the pull request the last
run observed, and drafts review rules from it. Takes no flags beyond the
global ones.
`

const postHelp = `redline post — post the session's findings as one PR review

usage:
  redline run --pr N
  redline post [flags]

Posts the last run's findings to the pull request it observed. The one
command that writes to GitHub.

flags:
  --pr N|URL        the pull request to post to; it must be the one the
                    session observed
  --branch, --commit, --range
                    refused: post targets the pull request the session
                    reviewed
  --report-url URL  link to the full report in the review body
  --profile PATH    YAML that stamps pass/fail markers a merge gate can read
                    (error and warning fail unless the file says otherwise).
                    Without it, post still comments and never approves.
  --dry-run         print the review payload instead of posting
`

const postmortemHelp = `redline postmortem — read back what the last review did

usage:
  redline postmortem [flags]

Shows what the last review proposed, what the lookups found, and what was
ruled.

flags:
  --format FMT      report|json (default report); json is the whole trace as
                    it was written
`

const openHelp = `redline open — serve and open the last report

usage:
  redline open [flags]

flags:
  --file            open the report as a file:// path, no server
  --port N          loopback port for the report server (default 8765; the
                    next free port is used if it is taken)
`

const serveHelp = `redline serve — serve --out over http

usage:
  redline serve [flags]

Blocks until stopped.

flags:
  --port N          loopback port (default 8765; the next free port is used
                    if it is taken)
  --stop            stop the server for --out
`

const gcHelp = `redline gc — remove this repo's cached review worktrees

usage:
  redline gc [flags]

Removes the worktrees a --commit, --branch, --range or --pr run cached under
~/.redline/worktrees for the current repository.

flags:
  --older-than D    only remove cached worktrees older than D (e.g. 168h)
`
