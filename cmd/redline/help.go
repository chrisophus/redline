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
	{name: "review", summary: "review a saved session with a model (--run observes first)",
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
var globalFlags = []string{"out", "session"}

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
  --out DIR         the session directory itself. Without it, each target's
                    session lives in .redline/sessions/<name>: pr-1360,
                    branch-feat-x, commit-<ref>, range-<a..b>, or
                    tree-<branch> for the working tree. A command given no
                    target works on the session the last run wrote.
  --session NAME    name the session under .redline/sessions instead of
                    deriving it from the target
  -h, --help        print this; after a command, that command's flags
`)
	return b.String()
}

func commandHelp(c command) string {
	for _, name := range registeredFlags(c) {
		if slices.Contains(globalFlags, name) {
			return c.help + "\nGlobal flags (--out, --session) are listed by `redline help`.\n"
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
	fs.StringVar(&o.session, "session", "", "name of the session under .redline/sessions")
}

func targetFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.pr, "pr", "", "GitHub pull request number or URL")
	fs.StringVar(&o.branch, "branch", "", "branch to review")
	fs.StringVar(&o.commit, "commit", "", "commit to review against its parent")
	fs.StringVar(&o.revRange, "range", "", "commit range A..B")
}

func browseFlags(fs *flag.FlagSet, o *opts) {
	fs.BoolVar(&o.open, "open", false, "open the HTML report when done")
	fs.IntVar(&o.port, "port", report.DefaultPort, "loopback port for the report server")
}

// observeFlags shape what a run observes. run always takes them; review takes
// them with --run.
func observeFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.base, "base", "", "base revision")
	fs.StringVar(&o.upstream, "upstream", "", "upstream branch for version-collision checks")
	fs.StringVar(&o.migDir, "migrations", "", "migrations directory")
	fs.BoolVar(&o.prepare, "prepare", false, "run harness produce steps from .redline.yml before observe")
	fs.BoolVar(&o.allowMissingCoverage, "allow-missing-coverage", false, "do not fail when a configured coverage profile is missing or stale")
	fs.BoolVar(&o.noLint, "no-lint", false, "skip lint delta, suppression, and configuration checks")
	fs.BoolVar(&o.noContext, "no-context", false, "gather no context beyond the diff")
}

func runFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	targetFlags(fs, o)
	observeFlags(fs, o)
	browseFlags(fs, o)
	fs.StringVar(&o.format, "format", "report", "report|json")
	fs.BoolVar(&o.file, "file", false, "print the report as a file:// path, no server")
	fs.BoolVar(&o.verbose, "verbose", false, "log each pane and context provider as it runs to stderr")
}

func reviewFlags(fs *flag.FlagSet, o *opts) {
	outFlag(fs, o)
	targetFlags(fs, o)
	observeFlags(fs, o)
	fs.BoolVar(&o.observe, "run", false, "observe the change and save its session before reviewing it")
	browseFlags(fs, o)
	fs.BoolVar(&o.stats, "stats", false, "print the recorded cost distribution and exit")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the assembled prompt and its estimated cost, and call nothing")
	fs.StringVar(&o.api, "api", "", "anthropic or openai")
	fs.StringVar(&o.baseURL, "base-url", "", "endpoint to send the call to, for a proxy")
	fs.StringVar(&o.apiUser, "api-user", "", "with --api openai: caller name for a proxy that wants one beside the key")
	fs.StringVar(&o.model, "model", "", "model to review and check the findings with")
	fs.StringVar(&o.effort, "effort", "", "low|medium|high|xhigh|max, for the review and the checking (default medium)")
	fs.StringVar(&o.scoutModel, "scout-model", "", "model to check the findings with, when it should differ from --model")
	fs.StringVar(&o.scoutEffort, "scout-effort", "", "effort for the checking, when it should differ from --effort")
	fs.StringVar(&o.synopsisModel, "synopsis-model", "", "model to write the walkthrough with, when it should differ from --model")
	fs.StringVar(&o.synopsisAPI, "synopsis-api", "", "wire for the describing call, when it should differ from --api")
	fs.StringVar(&o.synopsisBaseURL, "synopsis-base-url", "", "endpoint for the describing call, when it should differ from --base-url")
	fs.StringVar(&o.synopsisEffort, "synopsis-effort", "", "effort for the describing call, when it should differ from --effort")
	fs.StringVar(&o.since, "since", "", "the commit the previous review of this change ran against; the walkthrough then comes with a paragraph on what is new")
	fs.StringVar(&o.mode, "mode", "", "oneshot or explore")
	fs.IntVar(&o.maxTurns, "max-turns", 0, "with --mode explore: turn limit")
	fs.IntVar(&o.callTurns, "call-turns", 0, "turn cap for each describing/findings/ruling pass (default 30)")
	fs.IntVar(&o.samples, "samples", 0, "independent reviews to union")
	fs.StringVar(&o.note, "note", "", "what to look at or what worries you, added to every judging call")
	fs.StringVar(&o.noteFile, "note-file", "", "read the note from a file")
	fs.BoolVar(&o.deferContext, "defer-context", false, "list the resolved context beside each file's diff and let the reviewer read it with get_context")
	fs.BoolVar(&o.noLook, "no-look", false, "the judging pass writes from the material it was sent, looking nothing up")
	fs.BoolVar(&o.verify, "verify", false, "check each finding against the repository before posting it")
	fs.BoolVar(&o.noCache, "no-cache", false, "send every call at full input rate")
	fs.StringVar(&o.cacheTTL, "cache-ttl", "", "how long the cached prefix lives, 5m or 1h")
	fs.BoolVar(&o.noSynopsis, "no-synopsis", false, "one call writes the walkthrough and the findings together")
	fs.BoolVar(&o.reuseSynopsis, "reuse-synopsis", false, "reuse review.json's walkthrough instead of paying for a new describing call; refused if it is missing or stale")
	fs.IntVar(&o.cohorts, "cohorts", 0, "split the change into at most this many cohorts and judge each in its own call (default 1, no split)")
	fs.IntVar(&o.minCohortFiles, "min-cohort-files", 0, "below this many shown files a split run is one cohort (default 3)")
	fs.BoolVar(&o.noCrossSummaries, "no-cross-summaries", false, "each cohort reviews its files knowing nothing of the others")
	fs.BoolVar(&o.cohortContext, "cohort-context", false, "with --cohorts above 1: keep the resolved context out of the describing call and give each cohort only the context that belongs to its own files")
	fs.BoolVar(&o.planOnly, "plan", false, "with --cohorts above 1: describe and partition, then stop before judging any cohort")
	fs.StringVar(&o.onlyCohorts, "only-cohorts", "", "with --cohorts above 1: comma-separated cohort names or 1-based indices to judge, skipping the rest")
	fs.BoolVar(&o.verbose, "verbose", false, "log each model request, response, and scout tool call to stderr, and with --run each pane and provider")
	fs.BoolVar(&o.debug, "debug", false, "write the full requests and responses under --out/debug/<run timestamp>/")
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
	fs.BoolVar(&o.recap, "recap", false, "open the body with what changed since the previous review instead of repeating the walkthrough")
	fs.StringVar(&o.since, "since", "", "with --recap: the previous review's commit, when it cannot be read off that review's own marker")
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

Writes the session, findings.json, report.md and report.html into the
target's session directory (see ` + "`redline help`" + `), prints the markdown report,
and marks the session as the latest. Invokes no model.

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
  --no-context      gather no context beyond the diff: no providers, no
                    repository rules, no neighbouring files. For comparing a
                    review with context against one without.
  --open            open the HTML report when done
  --file            print the report as a file:// path, no server
  --port N          loopback port for the report server (default 8765; the
                    next free port is used if it is taken)
  --verbose         log each pane as it starts and finishes, with what it
                    found and how long it took, and the command line each
                    context provider ran. Without it a run prints the panes
                    it is about to run, each provider as it starts and
                    finishes, anything that did not run, and one line on
                    what it came to. REDLINE_VERBOSE does the same.
`

const reviewHelp = `redline review — review a saved session with a model and merge the result

usage:
  redline review [target flags] [flags]
  redline review --run [target flags] [flags]

Reviews a saved session. With no target flag it is the session the last run
wrote; with one it is that target's session, which has to exist. --run
observes the target first, as ` + "`redline run`" + ` would, saves its session, and
reviews that.

session:
  --run             observe the change and save its session before reviewing
  --pr N|URL        the pull request's session
  --branch REF      the branch's session
  --commit REF      the commit's session
  --range A..B      the range's session
  --base REF        with --run: base revision (default: commit parent, range
                    start, PR base, else origin/main)
  --upstream REF    with --run: branch new migrations must not collide with
  --migrations DIR  with --run: restrict migration checks to one directory
  --prepare         with --run: run harness produce steps from .redline.yml
  --allow-missing-coverage
                    with --run: do not fail when a configured coverage
                    profile is absent
  --no-lint         with --run: skip lint delta, suppression, and
                    configuration checks
  --no-context      with --run: gather no context beyond the diff

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
                    alike (default: medium for the review, low for the
                    checking). Named rather than left to the endpoint, whose
                    own default is high and is free to move, so the same
                    review on the same model costs and finds the same across
                    two SDK versions. Medium on measurement: reviews on this
                    model come back good at it, and the levels above cost more
                    without having shown they find more. Raise it with
                    --effort on a change that warrants it.
  --scout-model NAME
                    model to check the findings with, when it should differ
                    from --model
  --scout-effort LEVEL
                    effort for the checking, when it should differ from
                    --effort
  --synopsis-model NAME
                    model to write the walkthrough with, when it should
                    differ from --model. The describing call reads the diff
                    and writes a line per file, which is the cheapest thing
                    either call does, so it is the one worth moving to a
                    cheaper model. What it costs is priced where it was
                    spent and recorded on its own in the ledger, because a
                    token count carries no model and one usage record cannot
                    hold two rate cards. A model with no entry in the price
                    table makes the whole run's cost read as unknown rather
                    than as the judging call's alone.
  --synopsis-api WIRE
                    anthropic or openai for the describing call, when it
                    should differ from --api. Naming another wire here drops
                    --base-url, the key and --api-user for that one call
                    rather than sending them somewhere they do not belong;
                    give it --synopsis-base-url, or set OPENAI_API_KEY and
                    OPENAI_BASE_URL, which are read for it. Note that the
                    OpenAI wire marks no cache breakpoint, so a describing
                    call sent there writes no prefix for the judging call to
                    read back, and the judging call pays that write instead.
  --synopsis-base-url URL
                    endpoint for the describing call, when it should differ
                    from --base-url
  --synopsis-effort LEVEL
                    effort for the describing call, when it should differ
                    from --effort
  --since COMMIT    the commit the previous review of this change ran
                    against. The describing call is told which files have
                    moved since then and writes one paragraph on what is
                    new, beside the walkthrough rather than instead of it:
                    the overview and the file lines still describe the whole
                    change, because a reader arriving at the pull request
                    for the first time needs them and GitHub is not somewhere
                    the last review's file lines can be read back from.
                    redline post --recap is what puts that paragraph on the
                    pull request in place of repeating the walkthrough.
                    Resolved against this checkout, so a commit it cannot
                    find is refused rather than quietly ignored.
what to look at:
  --note TEXT       a note from you to the reviewer: which file worries you,
                    what to look at first, a question to answer. It goes at
                    the end of every judging call, after the material, and
                    not to the describing call or the ruling. It points the
                    reviewer's attention and is not evidence, so a finding
                    that rests on it says so, and a rule the repository
                    committed outranks it. Saved in review.json and the
                    postmortem, and shown on the report.
  --note-file PATH  the same, read from a file. Pass one or the other.
  --no-look         the judging pass writes from the material it was sent,
                    looking nothing up. Looking things up is the default: a
                    claim about code outside the diff is one the reviewer can
                    check while it writes rather than only name as a question
                    for the lookup pass, and a reviewer that pulls the context
                    it needs beats one handed a guess about what it would
                    want. Turn it off for a review that has to cost what a
                    plain one costs, or to measure against the shape without
                    the lookups. Five lookups, and a run offers the ones this
                    checkout can answer:
                      grep            search the repository
                      read_lines      read a span of one file
                      list_docs       what the team wrote down, so a
                                      deliberate construction is a decision
                                      you can find rather than a defect you
                                      file
                      symbol_context  one Go symbol's definition, its callers
                                      resolved through the type checker, its
                                      signature types and its tests. Needs
                                      gorefactor on PATH, and is left off the
                                      catalogue where it is missing
                      line_history    why a span of lines is there, from git,
                                      for a change that removes or rewrites
                                      code
                    It costs turns: three runs on one pull request put the
                    inline shape at 4.7x a plain review's cost, and --max-cost
                    is what holds that down. Result.Looked (printed as
                    looked=N) is what each run bought for it. A path climbing
                    out of the tree is
                    refused and a search is capped, the same hardening the
                    scout's own lookups have. A path .cursorindexingignore
                    names at the repository root is excluded from the search,
                    a direct read, the document list and the history lookup:
                    it says in as many words that a path is not for an
                    automated reader. .gitignore is not read for this -
                    generated code and vendored deps are routinely both
                    gitignored and something a claim needs to check against.
  --defer-context   leave the context the providers resolved out of the
                    prompt. Each file's diff is followed by an index
                    of the context that belongs to it, callers, types, tests
                    and history matched through the provider's scope, and
                    any pass reads an entry with get_context. The same
                    entries are offered as the prompt would carry.

shape:
  --mode MODE       oneshot (default) sends the context Redline chose;
                    explore sends a catalogue and lets the reviewer fetch
                    what it wants, costing more by design. In explore mode
                    --max-cost is a governor, not a tripwire.
  --max-turns N     with --mode explore: turn limit (default 5)
  --call-turns N    turn cap for each describing/findings/ruling pass over
                    the tool loop every mode runs (default 30). A pass that
                    looks things up spends turns before it writes anything,
                    and it spends them at the front, so the cap has to leave
                    room to look and then still write. The output budget is
                    what governs the money: a turn with no allowance left
                    ends the pass whatever this says.
  --samples N       take N independent reviews and union them (default 1).
                    Samples do not overlap, so recall rises with N and cost
                    rises with it too. With the cache on, the first goes out
                    alone and the rest follow once it has read the prompt, so
                    they read the prompt from the cache.
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
  --no-synopsis     one call writes the walkthrough and the findings
                    together. Describing and judging in two calls is the
                    default: they share a prefix, so the second reads what the
                    first cached, and what it buys is a walkthrough covering
                    every shown file plus an output cap the findings no longer
                    share with fifty file summaries. On the stored samples one
                    call in three above 34k tokens wrote no walkthrough at
                    all.
  --reuse-synopsis  reuse review.json's walkthrough (its overview and
                    per-file summaries) instead of paying for a new
                    describing call: the judging call still gets the
                    findings-alone contract a fresh walkthrough would give
                    it, at no describing-call cost. With --cohorts above 1,
                    also reuses the partition that call drew, so the whole
                    describing-and-partitioning stage is skipped and the run
                    goes straight to the cohort calls - combine with
                    --only-cohorts to re-judge one cohort alone, at that
                    cohort's full share of --max-tokens rather than the
                    fraction --cohorts divides it into. Refused when
                    review.json is missing, has no walkthrough, was written
                    against a different change, or (above --cohorts 1) has
                    no partition to reuse - a stale walkthrough or partition
                    on a new diff is a wrong report, not a saving. Cannot be
                    combined with --no-synopsis.
  --cohorts N       the split. 1 (default) judges the whole change in one
                    call. Above 1, the describing call also splits the shown
                    files into at most N cohorts, and each cohort is judged
                    in its own call over the same cached prefix. What it is
                    aimed at is measured: a reviewer with fifty files in
                    front of it spends its finding-count on the first few.
                    Stage one draws fewer when the change has fewer groups
                    in it; the tripwire prices the bound.
  --min-cohort-files N
                    below this many shown files a split run is one cohort
                    (default 3).
  --no-cross-summaries
                    each cohort is told nothing about its neighbours.
                    Cheaper, and gives up the correlations a cohort call
                    raises about a change it can see but was not given. By
                    default each cohort gets the other cohorts' summaries.
  --cohort-context  with --cohorts above 1: send the describing call no
                    resolved context at all, and give each cohort call only
                    the callers, types, tests and history that belong to
                    its own files rather than the whole change's. Written
                    into each cohort's own tail instead of the shared
                    prefix, since a block scoped to one cohort's files is
                    not a block any other cohort's call could read back -
                    the saving --defer-context and --cache buy on that
                    block is given up for it. Refused together with
                    --defer-context, which answers the same question a
                    different way, unless --plan already forces it off.
  --plan            with --cohorts above 1: describe the change and draw the
                    partition, then stop. No cohort call is sent, so
                    Comments comes back empty because nothing judged the
                    change, not because it was clean; the report and
                    Summary say plan-only rather than a finding count.
                    Refused without a split, which has no partition to
                    stop before. Always defers context, whether or not
                    --defer-context was also given: this one call has no
                    later call to read a fully-written context back from
                    cache, so writing it in full would only pay the
                    cache-write rate on tokens nothing amortizes.
  --only-cohorts LIST
                    with --cohorts above 1: judge only the cohorts stage one
                    drew that match one of a comma-separated list of
                    selectors, and skip the rest. Each is a 1-based index
                    into the partition as printed, a substring of a
                    cohort's name, or - only where no name matches - a
                    substring of one of its file paths, which is the one
                    identifier stable across separate runs since the name
                    is worded fresh each time. Combine with --plan to see
                    the partition first; a selector matching nothing is
                    refused rather than silently reviewing none of it.

cost and caching:
  --no-cache        send every call at full input rate. By default the
                    prompt the review and the ruling share is marked for the
                    prompt cache, so the second call reads it back instead of
                    paying for it again; the marking is ignored where the
                    write could not be read, which is --api openai.
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
                    3.00). A tripwire, not a governor: it sits above what an
                    ordinary change costs rather than near it, because a
                    tripwire that fires on ordinary work teaches whoever hits
                    it to pass --max-cost without reading the number.

output:
  --dry-run         print the assembled prompt and its estimated cost, and
                    call nothing
  --stats           print the cost distribution of the reviews recorded in
                    --out and exit. The target is an average, so this is the
                    number to read, not any single run.
  --verbose         log every model request, its stop reason and token
                    counts, the reasoning summary each turn showed, the
                    body the parser was handed, and each scout tool call
                    to stderr - what a person watching the run sees. With
                    --run it also logs the observing wave the way run
                    --verbose does. REDLINE_VERBOSE does the same.
  --debug           write the full requests and responses, with the
                    model's thinking summary and the exact instructions
                    sent with each call, under --out/debug/<run
                    timestamp>/ - what the run leaves behind for someone
                    to read afterward. Its own subdirectory per run, so
                    rerunning against the same --out never overwrites what
                    the last run captured. Independent of --verbose: a CI
                    run wants this on and --verbose off, since its own log
                    already has the run's stdout/stderr and only the
                    captured files survive as an artifact. REDLINE_DEBUG
                    does the same.
  --open            open the HTML report when done
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
  --pr N|URL        post that pull request's session (default: the session
                    the last run wrote, which has to be a pull request's)
  --branch, --commit, --range
                    refused: post targets the pull request the session
                    reviewed
  --report-url URL  link to the full report in the review body
  --profile PATH    YAML that stamps pass/fail markers a merge gate can read
                    (error and warning fail unless the file says otherwise).
                    Without it, post still comments and never approves.
  --recap           open the body with what changed since the previous
                    review instead of repeating the walkthrough. A pull
                    request reviewed four times carried four copies of a
                    walkthrough that had not changed; this replaces the
                    overview and the per-file table with the paragraph
                    redline review --since asked the describing call for.
                    The report still has all of it and the body still links
                    there. The previous review's commit is read off the
                    marker that review already carries, so it usually needs
                    no argument. Refused when this session has no such
                    paragraph, rather than posting the walkthrough again
                    under a heading that promises otherwise.
  --since COMMIT    with --recap: name the previous review's commit, for a
                    first recap post or a body whose marker was edited away
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
