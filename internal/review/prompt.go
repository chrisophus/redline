package review

import (
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/cover"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

// systemPrompt is the harness half: the output contract, the silence rules,
// and what makes a finding worth writing. It says nothing about any
// language. The language half arrives in the envelope's promptFragment,
// authored by whoever wrote the provider, and is concatenated below.
//
// What stays here is what the model cannot know from having read a great many
// reviews: that deterministic tools already ran and own the nits, that their
// findings are established, that connecting two of them is the finding no
// single producer can make, and that it must not assert what it was not shown.
// Everything about how to judge moved to judgingTail, which the stages that
// judge carry and the stages that describe do not.
//
// Not restating priors is what moves the model's attention off what the tools
// already caught and onto what static analysis structurally cannot see.
// Connecting two priors is named as valuable because a model will not
// volunteer it unless told the connection is the finding.
//
//go:embed prompts/system.md
var systemPrompt string

// judgingTail is what a call that writes findings is told, and it goes last,
// after the packet, because it is the instruction the model acts on rather
// than material it reads.
//
// It used to sit in the system block, which meant a call whose job was to
// describe the change read two thousand tokens on how to judge one before it
// read the diff. Splitting it is what lets the summary, the catalogue, a
// cohort review and the verifying pass share one prefix and carry only their
// own instruction.
//
// What was cut on the way out, and why each was dead weight:
//
// The seven question kinds and the confidence levels, which the output
// contract already describes in enums the endpoint enforces. Nine hundred
// tokens of prose restating a schema is attention spent on the one thing the
// model cannot get wrong.
//
// A six-item catalogue of what a defect looks like - nil dereferences,
// swallowed errors, broken callers - and a list of what not to say. Both
// teach a model trained on code reviews what it already knows, and the
// silence half read as a case for silence: the file's own history records
// that dropping it turned a weaker model into a padding machine, and keeping
// it cost recall.
//
// The convention rule, which said a construction the repository already uses
// is the team's convention and a finding against it is a finding against
// every file that does it. On a codebase written mostly by models that is an
// echo chamber with a rule behind it, and this reviewer is looking for bugs
// rather than conventions.
//
// What stays is measured or structural: enumerate every defect rather than
// picking one, which took a fixture from 1.44 comments and no catches to 4.67
// and 3 of 11 at one sample; zero findings being a valid answer, which is what
// keeps a reviewer from being a generator; and the question and verdict
// mechanisms, which are this system's own and nothing else would supply.
//
//go:embed prompts/judging.md
var judgingTail string

// describingTail is what a call that writes the walkthrough is told. The
// overview and the file lines are not findings, so none of the judging rules
// above apply to them, which is why they travel apart.
//
//go:embed prompts/describing.md
var describingTail string

// briefPrompt states the same job in a page. It once spelled out its own
// output contract in prose as well; the tool grammar carries that on both
// wires now, so the prose went. The numbers below were measured against the
// longer bytes, which git history has.
//
// The prompt and the emission were first measured together, on eleven fixtures
// at one sample on claude-sonnet-5 with the packet held constant: this prompt
// with a free-form reply caught 11 of 38 annotated defects, systemPrompt
// free-form 5 of 35, systemPrompt under the strict tools 6 of 38, and this
// prompt under the strict tools 2 of 38. Read as four points that cell is a
// cliff, and --brief shipped as the pair on the strength of it.
//
// It did not reproduce. Re-measured at three samples over the same eleven
// fixtures, free-form caught 17 of 38 and this prompt under the tools caught
// 15, against a noise floor near 3.6 expectations. The single fixture
// separating them, staged-empty-partition, then scored 1 of 3 against 0 of 3 at
// five samples, one catch in fifteen trials. At one sample 2/38 and 6/38 were
// never distinguishable either.
//
// What that leaves is the short prompt accounting for the recall, with the
// emission accounting for none of it that anything here can measure. So the
// emission is chosen on other grounds, and there is only one: completeOpenAI
// sends the catalogue on every call and has no way to be told otherwise, so a
// free-form review was never available on that wire. --brief is this prompt
// over the tool grammar, and both wires send the same request.
//
// It stopped being the default on 2026-09-14. Every figure above was taken
// through a local proxy that appended its own instructions to the system
// prompt, so none of them measured these bytes alone. Taken again directly,
// at three samples over fourteen fixtures, this prompt returned a stub reply,
// a placeholder in about 200 output tokens, on 22 of 168 calls and
// systemPrompt on none of 42. Recall could not rank the two: one
// configuration of this prompt caught 7, 8 and 14 of 38 on three runs.
//
// What it keeps is what earlier sweeps showed to be load-bearing: enumerate
// every defect rather than choosing one, and zero findings is a valid answer.
// What it drops is the catalogue of what is not worth reporting. Each clause
// of that catalogue was written against a real false positive and was
// defensible alone; together they read as a case for silence, and the model
// takes the case.
//
// What it once cost was output tokens, because nothing bounded a free-form
// reply the way a grammar does: the one live run of that shape, PR #46 of this
// repository at a 70,000-token ceiling, spent 32,795 output tokens on four
// findings and twelve file lines, $0.8382 all in with the lookups and the
// ruling. The grammar bounds it again, and the two arms came out at $0.1432 and
// $0.1499 mean per review over the eleven fixtures. --max-tokens still applies,
// and the cap arrives as a truncated object rather than a short one, so lower
// it carefully.
//
//go:embed prompts/brief.md
var briefPrompt string

// systemFor picks the harness half. briefPrompt applies to the review stage
// only: the ruling, the synopsis and the cohort partition each have a tool
// contract this block does not describe, and a stage that asked for one shape
// and was told to write another would fail to parse rather than review
// briefly.
func systemFor(opts Options, stage string) string {
	if opts.Brief && stage == StageReview {
		return briefPrompt
	}
	return systemPrompt
}

// oneShotAddendum tells the one-shot pass its material is complete. It sat in
// systemPrompt until explore mode inherited it there, and was told its context
// was complete in the same block that handed it a catalogue and a fetch tool.
//
// It also said the pass had no tools, which stopped being true when every stage
// began answering through the tool catalogue. A pinned call never noticed. A
// call asked to think is not pinned, and was told in one block that it had no
// tools and in the next to answer by calling one.
//
//go:embed prompts/oneshot-addendum.md
var oneShotAddendum string

// synopsisPrompt is the describing stage's own turn, appended after the shared
// prefix so the block the cache is keyed on does not move.
//
// It says what not to do twice, because the system block above spends most of
// its length teaching this model to find defects and a stage told to describe
// is being asked to ignore the bulk of its instructions. What it must not do
// is hedge the description into a review: a synopsis with findings in it
// spends the output budget this stage exists to free.
//
//go:embed prompts/synopsis.md
var synopsisPrompt string

// synopsisTail is the describing turn plus the roster of files it may write a
// line for.
//
// The roster is here because prose was not enough. The instruction already
// said "none of the files held back" and the first sweep still came back with
// ten lines about held-back test files - every invented line in the run was
// one. The change section names those files with their line counts and sends
// no diff, which reads to a model asked for a line per file as a file to
// write a line about. A list it can match against leaves nothing to infer.
//
// It costs paths, it goes in the tail behind the cache breakpoint, and it is
// built from the same predicate the score reads, so a change to what the
// prompt holds back moves the instruction, the diff and the measurement
// together.
func synopsisTail(in Input) string {
	return withRoster(synopsisPrompt, in)
}

// stepwisePrompt is turn 1 of the stepwise conversation. It differs from
// synopsisPrompt in what it can truthfully say about the material: this turn
// has the change and the diff and nothing else, and the rest arrives in the
// same conversation once the description is written.
//
//nolint:gosec // G101 reads the "pw" in "stepwise" as a password.
//go:embed prompts/stepwise.md
var stepwisePrompt string

// stepwiseLead opens turn 2's material, so the model reads what follows as the
// part of the packet its description was written without.
//
//nolint:gosec // G101 reads the "pw" in "stepwise" as a password.
//go:embed prompts/stepwise-lead.md
var stepwiseLead string

// stepwiseDescribeTail is turn 1's instruction with the same roster the
// describing call gets, for the same reason.
func stepwiseDescribeTail(in Input) string {
	return withRoster(stepwisePrompt, in)
}

// withRoster appends the files a describing instruction may write a line for.
func withRoster(prompt string, in Input) string {
	shown := in.ShownFiles()
	if len(shown) == 0 {
		return prompt
	}
	paths := make([]string, 0, len(shown))
	for path := range shown {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n### The files to describe\n\n")
	for _, path := range paths {
		b.WriteString("- " + path + "\n")
	}
	return b.String()
}

// findingsPrompt is the judging stage's turn when a synopsis already ran. The
// system block still describes the whole review, walkthrough included, because
// it is shared with the synopsis call byte for byte and a system block that
// varied per stage would cost the cache.
//
//go:embed prompts/findings.md
var findingsPrompt string

// cohortsTail is stage one's turn when the run fans out: describe, and draw
// the partition the fan-out reviews.
//
// The bound is stated rather than left to judgement because the tripwire
// priced the run at it. A stage one free to return nine cohorts would commit
// the run to nine calls it refused to pay for.
func cohortsTail(in Input, bound int) string {
	var b strings.Builder
	b.WriteString(synopsisTail(in))
	fmt.Fprintf(&b, `

### The cohorts

Also split the files above into at most %d group(s) best reviewed together,
and call the cohorts tool rather than the synopsis one. Every file belongs to
exactly one group and no group is empty.

Group by what a reviewer has to hold in mind at once: a schema change and the
code that reads it belong together across directories, and two unrelated
fixes in one package do not. Each group's summary is what the other groups'
reviewers see of it, so write it for someone who cannot see these lines.`, bound)
	return b.String()
}

// cohortTail is one stage-two call's turn: the same prefix as every other
// call, scoped by instruction to one cohort.
//
// Scoping is by instruction because it cannot be by input. The prefix carries
// every diff and is byte-identical on every call or nothing is cached, so a
// cohort call is told which files are its own while the rest stay in front of
// it at a tenth of the rate. That is the better arrangement anyway: a
// correlation this call raises against another cohort is grounded in lines it
// was shown rather than in somebody's summary of them.
func cohortTail(mine Cohort, all []Cohort, mineIdx int, crossSummaries bool) string {
	var b strings.Builder
	b.WriteString(findingsPrompt)
	fmt.Fprintf(&b, "\n\n### Your cohort: %s\n\n%s\n\nReview these files and only these:\n\n",
		mine.Name, strings.TrimSpace(mine.Summary))
	for _, path := range mine.Files {
		b.WriteString("- " + path + "\n")
	}
	// By index, not by name. Nothing makes stage one's names unique - the
	// contract asks for "two or three words" - and two cohorts it happens to
	// call the same thing would each drop the other from this list, so the
	// pair most likely to be related is the pair told nothing about each
	// other.
	others := make([]Cohort, 0, len(all))
	for i, c := range all {
		if i != mineIdx {
			others = append(others, c)
		}
	}
	if len(others) == 0 {
		return b.String()
	}
	if !crossSummaries {
		b.WriteString("\nOther reviewers have the rest of this change; " +
			"a defect outside your files is theirs to report.\n")
		return b.String()
	}
	b.WriteString("\n### The rest of the change\n\n" +
		"Another reviewer has each of these, and a defect inside one is theirs to report. " +
		"Their diffs are above. Raise one only where it bears on your own files, as a " +
		"correlation against the file of yours it affects.\n\n")
	for _, c := range others {
		fmt.Fprintf(&b, "- **%s** (%d file(s)): %s\n", c.Name, len(c.Files), strings.TrimSpace(c.Summary))
	}
	return b.String()
}

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
		if change.IsTestCode(f.Path) {
			continue
		}
		if f.Diff != "" || f.Head != "" {
			return true
		}
	}
	return false
}

// ShownFiles is the set of paths whose diffs the prompt actually carries,
// which is the set a walkthrough is expected to have a line for and no more.
//
// Exported because the score for walkthrough completeness is the share of
// these that came back with a summary, and a scorer with its own copy of
// "which files were shown" would eventually measure a rule the prompt does not
// have. There is one predicate and both sides read it.
func (in Input) ShownFiles() map[string]bool {
	if in.Change == nil {
		return nil
	}
	hideTests := in.hidesTests()
	shown := make(map[string]bool, len(in.Change.Files))
	for _, f := range in.Change.Files {
		if f.Diff == "" && f.Head == "" {
			continue
		}
		if hideTests && change.IsTestCode(f.Path) {
			continue
		}
		shown[f.Path] = true
	}
	return shown
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
			return x.Role == envelope.RoleTest || change.IsTestCode(x.File)
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
		if !change.IsTestCode(f.Path) {
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
		"The change is not untested; whether the tests are enough is measured by the checks. "+
		"Judge the code they test.",
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
		if hideTests && change.IsTestCode(f.Path) {
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
	b.WriteString(in.heardSection())
	b.WriteString(in.coverageSection())
	b.WriteString(in.absentSection())
	b.WriteString(in.diffSection())
	return b.String()
}

// heardSection is what this pull request already heard from Redline, and what
// the people reading it said back.
//
// It sits after the established findings and before everything else, because
// it is the same register as they are: things already on the record. The
// difference is who put them there, and the wording keeps that separate. A
// pane's finding is a measurement. A previous reviewer's finding is a previous
// reviewer's opinion, and an author's reply is the author's, which is why
// neither is presented as established.
//
// Two jobs. The first is not saying the same thing twice: two runs on one pull
// request posted one defect twice in different words, and no fingerprint could
// have caught that because a reviewer's fingerprint is its wording. The second
// is worth more. A reply saying "this mirrors the staging file next door" is a
// convention nobody wrote down, handed over by the one person who knows it, in
// the place the next review can be shown it.
func (in Input) heardSection() string {
	if len(in.Prior) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Already said on this pull request\n\n")
	b.WriteString("Redline left these comments on an earlier run, with what happened to each. " +
		"Do not raise any of them again.\n\n")
	b.WriteString("A reply is the author's position, not a ruling. Where it says something is " +
		"deliberate, treat that as this repository's convention for the rest of this review. " +
		"Where the diff or the context below contradicts them, say so once, as a new finding, " +
		"naming what you saw that they did not.\n\n")
	for _, t := range in.Prior {
		loc := t.File
		if t.Line > 0 {
			loc = fmt.Sprintf("%s:%d", t.File, t.Line)
		}
		if loc == "" {
			loc = "no line"
		}
		fmt.Fprintf(&b, "- %s — %s\n", loc, oneLine(t.Said))
		fmt.Fprintf(&b, "  outcome: %s\n", outcomeOf(t))
		for _, r := range t.Replies {
			who := r.Author
			if who == "" {
				who = "someone"
			}
			fmt.Fprintf(&b, "  %s replied: %s\n", who, oneLine(r.Body))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// outcomeOf says what became of a thread in the plainest words available.
//
// None of these signals is clean alone. A resolved thread with no reply means
// "fixed" and "dismissed with a click" equally, so it is reported as what was
// observed rather than as a conclusion drawn from it, and the reply text
// beside it is what actually carries the meaning.
func outcomeOf(t feedback.Thread) string {
	var parts []string
	switch {
	case t.Resolved && len(t.Replies) > 0:
		parts = append(parts, "answered and the thread closed")
	case t.Resolved:
		parts = append(parts, "the thread was closed without a reply, which may mean fixed or may mean dismissed")
	case len(t.Replies) > 0:
		parts = append(parts, "answered, thread still open")
	default:
		parts = append(parts, "nobody replied and nobody closed it")
	}
	if t.Outdated {
		parts = append(parts, "the lines it sat on have changed since")
	}
	switch {
	case t.Down > 0 && t.Up > 0:
		parts = append(parts, fmt.Sprintf("%d found it useful, %d did not", t.Up, t.Down))
	case t.Down > 0:
		parts = append(parts, fmt.Sprintf("%d marked it wrong", t.Down))
	case t.Up > 0:
		parts = append(parts, fmt.Sprintf("%d marked it useful", t.Up))
	}
	return strings.Join(parts, "; ")
}

// oneLine flattens a body so one thread is one entry. A reply pasted with its
// own newlines runs into the next bullet and the list stops being a list.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// build assembles the whole user-side prompt. The context block sits before
// the diff so the model reads what surrounds the change before the change.
func (in Input) build(budget envelope.Budgeted) string {
	var b strings.Builder
	b.WriteString(in.changeSection())
	b.WriteString(in.priorsSection())
	b.WriteString(in.heardSection())
	b.WriteString(in.coverageSection())
	b.WriteString(in.absentSection())
	if ctx := budget.Render(); ctx != "" {
		b.WriteString(in.contextHeader(keptRoles(budget)))
		b.WriteString(ctx)
		b.WriteString("\n")
	}
	b.WriteString(in.diffSection())
	return b.String()
}

// contextHeader introduces the context block, naming the roles it actually
// contains. The sentence used to enumerate Redline's six roles unconditionally,
// which made an exhaustive claim the block could contradict: a provider may
// ship a role Redline does not rank — the graph adapter's `neighbor` — and
// that block then arrived under a sentence saying every block was one of six
// other things. An unranked role is described by its own provider's
// promptFragment, so the honest header names it and leaves the words to the
// provider.
//
// It is priced with the fixed parts rather than counted against the block
// itself: it is written after FitAll has already fitted the expansions to the
// room left over, so leaving it out of the fixed total let the assembled
// prompt exceed the ceiling it was admitted under by the header's own cost.
// Pricing passes every role the envelopes carry and rendering passes the
// roles that survived, so the estimate errs high when a role is dropped.
func (in Input) contextHeader(roles []envelope.Role) string {
	var b strings.Builder
	b.WriteString("## Context beyond the diff\n")
	b.WriteString("\nResolved by " + providerNames(in.Envelopes) + ".")
	if named := describeRoles(roles); named != "" {
		b.WriteString(" Each block says what it is: " + named + ".")
	}
	b.WriteString("\n")
	return b.String()
}

// describeRoles renders the roles in rank order, glossing the ones Redline
// knows and naming the ones it does not.
func describeRoles(roles []envelope.Role) string {
	ordered := append([]envelope.Role(nil), roles...)
	// Rank order, so the sentence reads in the same order the blocks are
	// rendered in. Unranked roles sort last and tie on their own name, which
	// keeps the sentence stable across runs.
	sort.SliceStable(ordered, func(i, j int) bool {
		ri, _ := ordered[i].Rank()
		rj, _ := ordered[j].Rank()
		if ri != rj {
			return ri < rj
		}
		return ordered[i] < ordered[j]
	})
	// One pass: an unranked role sorts last above, so naming it last needs no
	// second slice.
	seen := map[envelope.Role]bool{}
	var parts []string
	for _, r := range ordered {
		if seen[r] {
			continue
		}
		seen[r] = true
		if g := r.Gloss(); g != "" {
			parts = append(parts, g)
			continue
		}
		parts = append(parts, "a block labelled "+string(r)+", which the provider's own note above describes")
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
}

// keptRoles is what survived the budget, and envelopeRoles is everything the
// providers offered. The header is rendered from the first and priced against
// the second.
func keptRoles(b envelope.Budgeted) []envelope.Role {
	out := make([]envelope.Role, 0, len(b.Kept))
	for _, x := range b.Kept {
		out = append(out, x.Role)
	}
	return out
}

func envelopeRoles(envs []*envelope.Envelope) []envelope.Role {
	var out []envelope.Role
	for _, e := range envs {
		if e == nil {
			continue
		}
		for _, x := range e.Expansions {
			out = append(out, x.Role)
		}
	}
	return out
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

// intentChars bounds what the author's own account of the change may take.
// A pull request body is usually a few hundred words; one that runs to pages
// is pasted output or a template, and the first part is the part that says
// what the change is for.
const intentChars = 2000

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
	b.WriteString(in.intentSection())
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

// intentSection is what the author said the change is for: the pull
// request's title and body when there is one, and the commit messages with
// their bodies. The body is where the reason lives, and until this was sent
// the model was told what the change was for by its subject lines alone.
//
// It is framed as a claim rather than as evidence - a description that says
// the error is handled does not handle it - and explicitly not as something
// to audit. Asking for prose-versus-code mismatches produced them: on a
// change with long rationale comments and a long pull request body, most of
// one review's findings were sentences to reword, which no reader can act
// on and which crowded out the code.
func (in Input) intentSection() string {
	var b strings.Builder
	if pr := in.Change.Target; pr != nil && pr.PR != nil && (pr.PR.Title != "" || pr.PR.Body != "") {
		fmt.Fprintf(&b, "Pull request #%d: %s\n", pr.PR.Number, strings.TrimSpace(pr.PR.Title))
		if body := strings.TrimSpace(pr.PR.Body); body != "" {
			b.WriteString(indent(clip(body, intentChars)) + "\n")
		}
		b.WriteString("\n")
	}
	if len(in.Change.Commits) > 0 {
		b.WriteString("Commits:\n")
		room := intentChars
		for _, c := range in.Change.Commits {
			b.WriteString("- " + strings.TrimSpace(c.Subject) + "\n")
			body := strings.TrimSpace(c.Body)
			if body == "" {
				continue
			}
			// Said rather than skipped, for the reason clip exists: a commit
			// whose message explained the change must not read like a commit
			// that had nothing to say. The budget is spent in commit order,
			// so this is the tail of a long series.
			if room <= 0 {
				b.WriteString(indent("[… message not shown, the budget for these was spent]") + "\n")
				continue
			}
			body = clip(body, room)
			room -= len(body)
			b.WriteString(indent(body) + "\n")
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "What the author says it does, in their words. It tells you what the change is " +
		"for. It is a claim, not evidence, and it is not itself under review: report what " +
		"the code does wrong, not where the prose and the diff disagree.\n\n" +
		b.String()
}

// clip bounds text at a character count on a line boundary where it can, and
// says that it did, so a cut description is not read as a short one.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > n/2 {
		cut = cut[:i]
	}
	return cut + "\n[… cut at " + strconv.Itoa(n) + " characters]"
}

// indent sets a block off from the prompt's own structure, so a description
// with headings of its own does not read as sections of the prompt.
func indent(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
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
		"Judge the source they were generated from.",
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
		b.WriteString(lintSentence(in.Report))
		return b.String()
	}
	var b strings.Builder
	b.WriteString("## Findings already established\n\n")
	b.WriteString("On the report already; do not restate them. ")
	b.WriteString("Reference one by putting its [id] in relatedFindings.\n\n")
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
	b.WriteString(lintSentence(in.Report))
	if len(in.Report.Unknowns) > 0 {
		b.WriteString("Not determined by any check:\n")
		for _, u := range in.Report.Unknowns {
			fmt.Fprintf(&b, "- %s: %s\n", u.Substrate, u.Message)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// lintSentence names the linters that ran over this change, so the reviewer
// leaves their ground to them. The system prompt used to say a linter had run
// on every change, which was false wherever the lint check did not run, and a
// reviewer told so set aside exactly the defects nothing had looked for. Empty
// when no linter ran: saying nothing claims nothing.
func lintSentence(r *findings.Report) string {
	var names []string
	for _, t := range r.Tools {
		if (t.Status == "ran" || t.Status == "degraded") && !slices.Contains(names, t.Name) {
			names = append(names, t.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "Linters ran over this change: " + strings.Join(names, ", ") +
		". Leave what they catch to them.\n\n"
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
		"finding, not to report a number. Look at the lines marked error handling " +
		"first.\n\n" +
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
	b.WriteString("\nThese areas are unexamined; their silence is not a pass.\n\n")
	return b.String()
}

func (in Input) diffSection() string {
	if in.Change == nil || len(in.Change.Files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The diff\n\n")
	b.WriteString("A small file is given whole, as it is after the change, with the changed " +
		"line ranges named and any removed lines listed above it. Larger files are shown " +
		"as a diff.\n\n")
	shown := in.ShownFiles()
	for _, f := range in.Change.Files {
		if !shown[f.Path] {
			// Either nothing to show, or test code named in the change
			// section with its line counts, which is all a review of the
			// code under test needs from it.
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
