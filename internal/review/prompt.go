package review

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// systemPrompt is the harness half: the output contract, the silence rules,
// and what makes a finding worth writing. It says nothing about any
// language. The language half arrives in the envelope's promptFragment,
// authored by whoever wrote the provider, and is concatenated below.
//
// Four instructions here carry most of the weight.
//
// Not restating priors is what moves the model's attention off what the
// tools already caught and onto what static analysis structurally cannot
// see. Connecting two priors is named as valuable because it is the one
// thing no single producer can do, and a model will not volunteer it unless
// told the connection is the finding.
//
// Reporting every defect rather than the most important one was added on
// measurement, and it is the largest single effect found so far. Against a
// real change carrying eleven defects a reviewer could reach from the
// material, the silence rules alone produced 1.44 comments per run and
// caught none of them at one sample; naming enumeration as the job took it
// to 4.67 comments and 3 of 11 at one sample, and 7 of 11 at three, which
// beat nine samples of the old wording at under half the cost and with no
// rise in unmatched comments. A reviewer that finds one defect and stops has
// failed the author as surely as one that pads.
//
// Zero findings being valid is the hardest of the four to get and the one
// most homegrown reviewers miss. A reviewer that always finds something is
// not a reviewer, it is a generator, and the first time it invents a problem
// on a clean change is the last time anyone reads its output. It sits beside
// the enumeration instruction rather than being replaced by it: the two are
// the same rule, which is to report what is there and no more. Dropping this
// half is what turned a weaker model into a padding machine in the same
// experiment, at 41 comments and 32 unmatched.
const systemPrompt = `You are reviewing one change in a code repository, once, in a single pass.

You have no tools. Everything you get to see is below. If a question cannot be
answered from what is here, do not guess at it and do not raise it: say nothing
about it. An unanswerable question raised as a finding costs the reader more
than it saves.

You are given findings that deterministic tools already produced for this
change. Treat them as established and already on the report.

- Do not restate them. A finding that repeats one of them is worse than no
  finding, because it makes the reader read the same thing twice and trust the
  list less.
- Connecting two of them IS a finding, and it is the most valuable thing you
  can produce here. A migration that adds a non-nullable column and a struct
  field that cannot express absence are each unremarkable alone. Together they
  say the write path is about to break. When you make that connection, set
  category to "correlation" and put the fingerprints of both priors in
  relatedFindings.
- Reference a prior by its fingerprint rather than describing it again.

What is worth reporting, given tools have already run:

- The change does not do what its commits and its shape say it does.
- An invariant that holds elsewhere in this code no longer holds here.
- An error path that cannot be reached, or one that is reached and swallowed.
- A caller you were shown that this change breaks.
- Two facts in the material below that contradict each other.
- A change that undoes an earlier deliberate fix, when the history shows one.

What is not worth reporting:

- Style, formatting, and naming, unless the change makes the code wrong.
- Anything a linter would catch. One already ran.
- Test coverage as a number. That is measured elsewhere. A specific line the
  change added that nothing executes is different: say what breaks if it is
  wrong, or say nothing.
- Praise, summaries of what the diff plainly shows, or advice to "consider"
  something without saying what breaks if it is not done.
- Anything you would qualify with "may", "might", or "could potentially" and
  cannot follow with a concrete consequence.

This change may carry several independent defects. Report every one you can
support, each as its own comment, rather than choosing the most important.
A reviewer that reports one defect and stops has failed the reviewer's job as
badly as one that pads: the author cannot fix what nobody named. Do not
invent findings, and do not report style or anything a linter caught, but do
not stop at the first thing either. Ten defensible findings on a change that
has ten is the correct answer, and zero on a change that has none is equally
correct: roughly three in ten real changes deserve no comment at all, and on
one of those you return an empty comments array and say so in the overview.

Set confidence honestly. Report a finding you are unsure of with confidence
"low" rather than withholding it. Low-confidence findings are folded away on
the report, so an uncertain finding costs the reader nothing and a withheld
one costs them the finding.

Write plainly. One or two sentences per comment, naming the specific thing and
what happens because of it.

Besides the comments, say what the change is. Two fields carry that, and they
are not findings, so none of the rules above about what is worth reporting
applies to them.

The overview is one or two paragraphs on what this change does and why it
exists, read off the commits, the shape of the diff, and the context you were
given. Someone who has not opened the diff should be able to read it and know
what landed. If the change is clean, say that here; it is the one place a
review with no comments still tells the reader something.

The files array is one line per file on what that file's change does. Give a
line for every file whose diff you were shown, and none for the ones held back
above: their diffs are not here, so anything you said about them would be
invention. Say what changed and why, not what the diff plainly is. "Holds the
graph's build revision so a stale graph can be reported" beats "adds a field
to Graph".

The verdicts array is where you rule on the findings the checks already made.
You have the whole diff and the context beyond it; the check that fired had a
pattern. So you can tell what it could not:

- should-fix when it is right and the code should change. Put the fix in the
  fix field.
- justified when what it flags is deliberate and correct here, and say what
  makes it so.
- rule-noisy when the check is wrong here, or fires too often to be worth
  reading.

Rule only where you have something the check did not. A verdict that restates
the finding is worse than no verdict: it costs the reader a line and tells
them nothing. An empty verdicts array is the right answer when the findings
speak for themselves, and most of the time they do.`

// hidesTests reports whether the request holds the change's test files back.
//
// Test code is the biggest thing a review can be sent that it was not asked
// to judge. On this repository's own changes it is routinely half the diff,
// and the half a reviewer told not to comment on coverage has the least to do
// with. Whether the tests are adequate is measured, by the coverage pane and
// by mutation, and those answers arrive as priors. Sending the test bodies
// too buys a second opinion on a question already answered, at the price of
// the context that would have paid for a correlation finding.
//
// The exception is a change that is only tests. There the tests are the
// change, and a review shown nothing is not a review, so they are sent.
func (in Input) hidesTests() bool {
	if in.Change == nil {
		return false
	}
	for _, f := range in.Change.Files {
		if change.IsTest(f.Path) {
			continue
		}
		if f.Diff != "" || f.Head != "" {
			return true
		}
	}
	return false
}

// contextFilter keeps test code out of the context block too. A provider
// resolves a test that covers a changed symbol because the contract asks it
// to; whether this review pays for it is Redline's call, and it is the same
// call the diff section makes.
func (in Input) contextFilter() envelope.Filter {
	if !in.hidesTests() {
		return envelope.Filter{}
	}
	return envelope.Filter{
		What: "test code",
		Drop: func(x envelope.Expansion) bool {
			return x.Role == envelope.RoleTest || change.IsTest(x.File)
		},
	}
}

// testsLine is what the held-back test files get: their names and how much
// moved in them. The same bargain generated files get. Naming them is what
// keeps the exclusion visible, and a reviewer that is not told the tests
// exist will write "this is untested" about a change that is not.
func (in Input) testsLine() string {
	if in.Change == nil || !in.hidesTests() {
		return ""
	}
	var paths []string
	var added, removed int
	for _, f := range in.Change.Files {
		if !change.IsTest(f.Path) {
			continue
		}
		paths = append(paths, f.Path)
		added += f.Added
		removed += f.Removed
	}
	if len(paths) == 0 {
		return ""
	}
	return fmt.Sprintf("%d test file(s) also changed (+%d -%d) and are not shown: %s. "+
		"They moved, so the change is not untested. Whether what they assert is enough is measured "+
		"by the checks whose findings you were given, not read here. Judge the code they test.",
		len(paths), added, removed, strings.Join(paths, ", "))
}

// shownLines is every line of the change the diff section already puts in
// front of the model, read from the unified-diff hunk headers.
//
// This is the language-agnostic half of a language-specific problem. A
// provider resolves what surrounds a change without knowing how the change
// will be presented, so it cannot tell that the enclosing declaration it
// found is a function in a brand new file the diff already shows in full.
// Redline can, from the hunk headers alone, for any language.
func (in Input) shownLines() envelope.Seen {
	seen := envelope.Seen{}
	if in.Change == nil {
		return seen
	}
	hideTests := in.hidesTests()
	for _, f := range in.Change.Files {
		if hideTests && change.IsTest(f.Path) {
			// Held back below, so nothing in it has been shown. Marking it
			// seen would suppress expansions on the grounds that the model
			// had already read lines it was never sent.
			continue
		}
		if f.Head != "" {
			// The whole file reaches the model, so every expansion inside it
			// is already shown and must not be sent twice.
			for i := 1; i <= strings.Count(f.Head, "\n")+1; i++ {
				seen.Add(f.Path, i)
			}
		}
		for _, line := range strings.Split(f.Diff, "\n") {
			if !strings.HasPrefix(line, "@@") {
				continue
			}
			start, count, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			for i := 0; i < count; i++ {
				seen.Add(f.Path, start+i)
			}
		}
	}
	return seen
}

// parseHunkHeader reads the "+start,count" half of a unified diff hunk
// header. A hunk with no count covers one line.
func parseHunkHeader(line string) (start, count int, ok bool) {
	i := strings.Index(line, "+")
	if i < 0 {
		return 0, 0, false
	}
	rest := line[i+1:]
	if j := strings.IndexAny(rest, " @"); j >= 0 {
		rest = rest[:j]
	}
	startStr, countStr, hasCount := strings.Cut(rest, ",")
	start, err := strconv.Atoi(startStr)
	if err != nil || start < 1 {
		return 0, 0, false
	}
	count = 1
	if hasCount {
		count, err = strconv.Atoi(countStr)
		if err != nil || count < 0 {
			return 0, 0, false
		}
	}
	return start, count, true
}

// fixed is everything the prompt must carry whatever the budget says: what
// changed, what the tools already found, what did not run, and the diff
// itself. A review without the diff is not a review, so these are priced
// first and the context gets what is left.
func (in Input) fixed() string {
	var b strings.Builder
	b.WriteString(in.changeSection())
	b.WriteString(in.priorsSection())
	b.WriteString(in.coverageSection())
	b.WriteString(in.absentSection())
	b.WriteString(in.diffSection())
	return b.String()
}

// build assembles the whole user-side prompt. The context block sits before
// the diff so the model reads what surrounds the change before the change.
func (in Input) build(budget envelope.Budgeted) string {
	var b strings.Builder
	b.WriteString(in.changeSection())
	b.WriteString(in.priorsSection())
	b.WriteString(in.coverageSection())
	b.WriteString(in.absentSection())
	if ctx := budget.Render(); ctx != "" {
		b.WriteString(in.contextHeader())
		b.WriteString(ctx)
		b.WriteString("\n")
	}
	b.WriteString(in.diffSection())
	return b.String()
}

// contextHeader introduces the context block. It is priced with the fixed
// parts rather than counted against the block itself: it is written after
// FitAll has already fitted the expansions to the room left over, so leaving
// it out of the fixed total let the assembled prompt exceed the ceiling it
// was admitted under by the header's own cost.
func (in Input) contextHeader() string {
	var b strings.Builder
	b.WriteString("## Context beyond the diff\n")
	b.WriteString("\nResolved by " + providerNames(in.Envelopes) + ". Each block says what it is: ")
	b.WriteString("an enclosing declaration, a caller of something this change touched, ")
	b.WriteString("a type in a changed signature, a sibling implementation, a test, or prior history of these lines.\n")
	return b.String()
}

func providerNames(envs []*envelope.Envelope) string {
	var names []string
	for _, e := range envs {
		if e != nil {
			names = append(names, providerName(e))
		}
	}
	if len(names) == 0 {
		return "the context provider"
	}
	return strings.Join(names, " and ")
}

func providerName(e *envelope.Envelope) string {
	if e == nil || e.Provider.Name == "" {
		return "the context provider"
	}
	if e.Provider.Version == "" {
		return e.Provider.Name
	}
	return e.Provider.Name + " " + e.Provider.Version
}

func (in Input) changeSection() string {
	var b strings.Builder
	b.WriteString("## The change\n\n")
	if in.Change == nil {
		b.WriteString("No file-level detail was available.\n\n")
		if gen := in.generatedLine(); gen != "" {
			b.WriteString(gen + "\n\n")
		}
		return b.String()
	}
	if len(in.Change.Commits) > 0 {
		b.WriteString("Commits:\n")
		for _, c := range in.Change.Commits {
			b.WriteString("- " + strings.TrimSpace(c.Subject) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Files:\n")
	for _, f := range in.Change.Files {
		fmt.Fprintf(&b, "- %s (%s, +%d -%d)\n", f.Path, f.Status, f.Added, f.Removed)
	}
	b.WriteString("\n")
	if gen := in.generatedLine(); gen != "" {
		b.WriteString(gen + "\n\n")
	}
	if tests := in.testsLine(); tests != "" {
		b.WriteString(tests + "\n\n")
	}
	return b.String()
}

// generatedLine is the whole of what generated files get: a count. Pasting
// them in would spend thirty thousand tokens on text no reviewer would ever
// act on, and dropping them silently would hide that they moved at all.
func (in Input) generatedLine() string {
	var paths []string
	if in.Report != nil {
		paths = append(paths, in.Report.Coverage.Generated...)
	}
	for _, e := range in.Envelopes {
		if e != nil {
			paths = append(paths, e.GeneratedFiles()...)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	uniq := paths[:0]
	var last string
	for _, p := range paths {
		if p != last {
			uniq = append(uniq, p)
			last = p
		}
	}
	return fmt.Sprintf("%d generated file(s) also changed and are not shown: %s. "+
		"They are machine output. Judge the source they were generated from, not them.",
		len(uniq), strings.Join(uniq, ", "))
}

// priorsSection is wave one's output, marked as already known. Each finding
// carries its fingerprint, which is the id wave two references it by.
//
// A missing report and a report with nothing in it are different facts, and
// the model acts on the difference: "the checks found nothing" is evidence
// about the change, and a reviewer told that will not raise what the checks
// cover. When there is no report at all, nothing was checked, and saying
// otherwise invents a clean result out of an absent one.
func (in Input) priorsSection() string {
	if in.Report == nil {
		var b strings.Builder
		b.WriteString("## Findings already established\n\n")
		b.WriteString("The deterministic findings were not available for this review.\n\n")
		return b.String()
	}
	if len(in.Report.Findings) == 0 {
		var b strings.Builder
		b.WriteString("## Findings already established\n\n")
		b.WriteString("None. The deterministic checks that ran found nothing to report.\n\n")
		return b.String()
	}
	var b strings.Builder
	b.WriteString("## Findings already established\n\n")
	b.WriteString("These are on the report already. Do not restate them. ")
	b.WriteString("Each is written as [id]; put that id in relatedFindings to reference it.\n\n")
	for _, f := range in.Report.Findings {
		if f.Source == findings.SourceLLM {
			// A previous reviewer's remark is not an established fact and
			// must not be presented to this one as though it were.
			continue
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if loc == "" && f.Anchor != nil {
			loc = f.Anchor.Kind + " " + f.Anchor.ID
		}
		fmt.Fprintf(&b, "- [%s] %s · %s · %s\n  %s\n",
			f.ID, f.Severity, f.Substrate, loc, f.Message)
		if f.Observed != "" {
			fmt.Fprintf(&b, "  observed: %s\n", f.Observed)
		}
	}
	b.WriteString("\n")
	if len(in.Report.Unknowns) > 0 {
		b.WriteString("Not determined by any check:\n")
		for _, u := range in.Report.Unknowns {
			fmt.Fprintf(&b, "- %s: %s\n", u.Substrate, u.Message)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// coverageSection is the lines this change added that no test executes.
//
// Not the percentage: that is the coverage pane's answer and the prompt tells
// the reviewer not to repeat it. What a number cannot say is which line, and
// that is the difference between "coverage went down" and "the error path you
// just added is the one nothing runs". Added lines only, because an old
// uncovered line in a file this change touched is not this change's news.
func (in Input) coverageSection() string {
	if len(in.LineCoverage) == 0 || in.Change == nil {
		return ""
	}
	var b strings.Builder
	files, omitted := 0, 0
	for _, f := range in.Change.Files {
		lines, ok := in.LineCoverage[f.Path]
		if !ok || len(lines) == 0 {
			continue
		}
		var uncovered, onErrorPath []int
		text := cover.AddedLineText(f.Diff)
		for _, line := range cover.AddedLines(f.Diff) {
			if covered, known := lines[line]; !known || covered {
				continue
			}
			uncovered = append(uncovered, line)
			if handlesError(text[line]) {
				onErrorPath = append(onErrorPath, line)
			}
		}
		if len(uncovered) == 0 {
			continue
		}
		if files >= maxCoverageFiles {
			omitted++
			continue
		}
		files++
		sort.Ints(uncovered)
		fmt.Fprintf(&b, "- %s: %s", f.Path, lineRanges(uncovered))
		if len(onErrorPath) > 0 {
			sort.Ints(onErrorPath)
			fmt.Fprintf(&b, " (error handling: %s)", lineRanges(onErrorPath))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "- and %d more file(s) with uncovered added lines\n", omitted)
	}
	return "## Added lines no test executes\n\n" +
		"From the coverage profile, for the files it covers. Use it to sharpen a " +
		"finding you already have, not to report a number. Lines marked error " +
		"handling are the ones worth looking at first: an error path nothing " +
		"exercises is the case that fails in production and not in CI.\n\n" +
		b.String() + "\n"
}

// These bound a section that sits with the facts rather than with the context,
// so it is priced first and never truncated. On a change that rewrites a
// package every added line is uncovered until the tests land, and unbounded
// that is hundreds of ranges taking the ceiling from the context block that
// would have paid for a finding. What is cut is counted, not hidden.
const (
	maxCoverageFiles  = 15
	maxCoverageRanges = 12
)

// handlesError reports whether a line looks like error handling.
//
// Text patterns rather than a parser: this package links no language
// toolchain, the same rule the rest of Redline follows, and the test-delta
// pane already reads t.Skip and .only the same way. Conservative on purpose.
// A miss puts the line in the ordinary list, which is where it would have
// been anyway; a false positive points the reviewer at a line that turns out
// to be unremarkable, which costs it a look.
func handlesError(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	switch {
	case strings.Contains(t, "if err != nil"), strings.Contains(t, "if err !="):
		return true
	case strings.HasPrefix(t, "return") && strings.Contains(t, "err"):
		return true
	case strings.Contains(t, "fmt.Errorf("), strings.Contains(t, "errors.New("):
		return true
	case strings.Contains(t, "panic("):
		return true
	case strings.HasPrefix(t, "throw "), strings.HasPrefix(t, "raise "):
		return true
	case strings.Contains(t, "catch ("), strings.Contains(t, "} catch"):
		return true
	case strings.HasPrefix(t, "except "), strings.HasPrefix(t, "except:"), strings.HasPrefix(t, "rescue"):
		return true
	}
	return false
}

// lineRanges folds a sorted line list into ranges, because "44-71" is one
// thing a reader can hold and twenty-eight numbers are not.
func lineRanges(lines []int) string {
	var parts []string
	for i := 0; i < len(lines); {
		if len(parts) == maxCoverageRanges {
			parts = append(parts, fmt.Sprintf("and %d more line(s)", len(lines)-i))
			break
		}
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[j]+1 {
			j++
		}
		if j == i {
			parts = append(parts, fmt.Sprintf("%d", lines[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", lines[i], lines[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

// absentSection names producers that did not run. A producer that errored
// degrades to a missing input rather than blocking the review, and the model
// has to be told which inputs are missing: a check that did not run and a
// check that came back clean look identical from here.
func (in Input) absentSection() string {
	if len(in.Absent) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Checks that did not run\n\n")
	for _, a := range in.Absent {
		b.WriteString("- " + a + "\n")
	}
	b.WriteString("\nTreat these areas as unexamined. Their silence is not a pass.\n\n")
	return b.String()
}

func (in Input) diffSection() string {
	if in.Change == nil || len(in.Change.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The diff\n\n")
	b.WriteString("Where a file is small enough it is given whole, at its state after the " +
		"change, with the changed line ranges named and any removed lines listed above it. " +
		"The invariant a change breaks usually lives in the part of the file the change did " +
		"not touch, and the added lines are already in the file, so repeating them as a diff " +
		"would only send them twice. Larger files are shown as a diff instead.\n\n")
	hideTests := in.hidesTests()
	for _, f := range in.Change.Files {
		if f.Diff == "" && f.Head == "" {
			continue
		}
		if hideTests && change.IsTest(f.Path) {
			// Named in the change section with its line counts, and that is
			// all a review of the code under test needs from it.
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", f.Path)
		if f.Head == "" {
			// No whole file, so the diff is the only view of this one.
			if f.Diff != "" {
				fmt.Fprintf(&b, "```diff\n%s\n```\n\n", strings.TrimRight(f.Diff, "\n"))
			}
			continue
		}
		// The whole file is below, so every added line is already about to
		// be sent. Repeating the unified diff sends it twice, which on a
		// change that is mostly additions is most of the prompt. What the
		// file cannot show is what left and where the edits landed, so that
		// is what the diff is reduced to.
		ranges, removed := changeShape(f.Diff)
		if len(ranges) > 0 {
			fmt.Fprintf(&b, "Changed lines: %s.\n", strings.Join(ranges, ", "))
		}
		if len(removed) > 0 {
			b.WriteString("\nRemoved by this change, so no longer in the file below:\n\n```\n")
			for _, r := range removed {
				b.WriteString(r + "\n")
			}
			b.WriteString("```\n")
		}
		fmt.Fprintf(&b, "\nThe file after the change:\n\n```%s\n%s\n```\n\n",
			f.Language, strings.TrimRight(f.Head, "\n"))
	}
	return b.String()
}

// changeShape reduces a unified diff to what a whole file cannot say: which
// line ranges the change touched, and the lines it removed.
//
// Additions are dropped on purpose. They are in the file that follows, and on
// a change that is mostly new code they are nearly the whole diff.
func changeShape(diff string) (ranges []string, removed []string) {
	var oldLine int
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			if start, count, ok := parseHunkHeader(line); ok {
				switch count {
				case 0:
					ranges = append(ranges, fmt.Sprintf("at %d", start))
				case 1:
					ranges = append(ranges, strconv.Itoa(start))
				default:
					ranges = append(ranges, fmt.Sprintf("%d-%d", start, start+count-1))
				}
			}
			oldLine = parseOldStart(line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
		case strings.HasPrefix(line, "-"):
			removed = append(removed, fmt.Sprintf("%d: %s", oldLine, line[1:]))
			oldLine++
		case strings.HasPrefix(line, "+"):
		default:
			oldLine++
		}
	}
	return ranges, removed
}

// parseOldStart reads the "-start" half of a hunk header, which is where the
// removed lines are numbered from.
func parseOldStart(line string) int {
	i := strings.Index(line, "-")
	if i < 0 {
		return 0
	}
	rest := line[i+1:]
	if j := strings.IndexAny(rest, " ,@+"); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return n
}
