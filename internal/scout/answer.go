package scout

import (
	"fmt"
	"strings"
)

// Answering is the scout's second job, and it is the one it is better at.
//
// Scouting proper reads a diff and guesses what the reviewer will want. That
// guess is made before anything has been reviewed, so it can only be about the
// change's shape: a signature moved, so fetch callers. It cannot know that the
// reviewer will end up doubting whether a hard-coded column list is normal
// here, because nobody has doubted it yet.
//
// Answering runs after a review exists. Each finding arrives with the one
// check that would confirm or refute it, written by the model that made the
// claim, so there is no guessing left to do. The lookups are the same lookups:
// a grep, a read, a caller query, a git log. What changed is that somebody
// asked for them.
//
// The rule that holds the whole design up holds here too. The scout records a
// location and this program reads the bytes, so an answer is the repository's
// own text under a claim about where to find it, never a model's recollection
// of what it found.

// Question is one thing a finding needs looked up before anyone reads it.
type Question struct {
	// ID is the finding this question belongs to. Echoed back on every record
	// so the ruling can tell which answer settles which claim.
	ID string
	// Kind is one of the closed set the review's schema enforces: precedent,
	// caller, rule, history, type. The kinds that need no lookup never reach
	// here.
	Kind string
	// Ask is the question in words, and Subject is the one thing to look up.
	Ask     string
	Subject string
	// Claim is what the finding says, so the scout knows what it is checking
	// rather than searching blind. It is deliberately not asked to judge the
	// claim: a scout that starts forming opinions stops fetching.
	Claim string
	File  string
	Line  int
}

// answerPrompt is the brief for a scout that is checking claims rather than
// exploring a change.
//
// Two things in it are load-bearing. The scout still does not review: it is
// told what each finding claims so it can search for the right thing, and told
// in the same breath that ruling on the claim is somebody else's turn. And a
// question it cannot answer has to come back as a note saying so, because the
// stage after this one treats silence and absence differently: an unanswered
// question makes a finding unverifiable and folds it away, where a wrong
// silence would let it through as though nothing had been asked.
func answerPrompt(tools []string, turns int) string {
	return fmt.Sprintf(`You are checking a code review's findings before anyone reads them.

A model reviewed a change and wrote down, for each thing it flagged, the one
lookup that would confirm or refute it. Your job is to run those lookups and
put what you find in front of the model that rules on them next. You do not
rule. You do not review. You find the evidence and record where it is.

You are not writing the evidence. You record a file and a line range, and the
program reads those bytes from the repository itself. So never retype code,
never summarise a function, never describe what something does. Record where
it is, and tag the record with the id of the question it answers.

How to answer each kind:

- precedent: does this repository already do the same thing elsewhere, in code
  this change did not touch? Grep for the pattern or the symbol. Record the
  clearest one or two places that do it, and say in your notes how many you
  found. This is the kind that matters most: a finding against something the
  repository does everywhere is a finding against the whole repository, and
  the reviewer had no way to know.
- caller: what calls or reads the thing that changed? Use gorefactor when the
  language is Go and it is available, because its callers are resolved rather
  than matched by name. Otherwise grep. Record the call sites that would
  actually break if the claim is right. When the question is what the called
  thing does with what it is given, the answer is in the callee and the call
  site only says where to look: record the body that uses it.
- rule: did the team write this down? Look in the repository's own rules and
  design notes with list_docs and read_lines, and record the specific lines
  that bear on the claim, not the whole file.
- history: why was the removed code there? Record the lines under the history
  role and git will be asked what happened to them.
- type: what can this type represent? Record the declaration.

Evidence against the finding is worth more than evidence for it. You are
trying to save the author from reading something wrong, so a search that
turns up thirty files already doing the flagged thing is the most useful
result you can return, and you should record it plainly and say the count.

If you cannot settle a question, say so in your notes for that finding, in one
sentence, naming what you looked for. A finding nobody could check is folded
away rather than posted, so an honest "no precedent found for X, searched the
whole tree" and a silence are read very differently: the first is an answer.
Start every note with the id of the question it is about, in brackets, the
way the brief writes it: the ruling reads the notes beside the findings and
a note that names no finding is a note it cannot place.

Some questions are about code that is not in this repository: what a library
does with what it is handed, what a dependency's type can hold. Your tools
search this tree and nothing else, so an empty grep says nothing either way
about any of that. Say which it is in the note, in as many words: "this is in
anthropic-sdk-go, outside the tree, so it could not be checked here" is an
answer the ruling can use, and an empty search reported as a negative is one
it cannot.

Tag every record with the id of the question it answers. A record with no id
reaches the ruling as something found but tied to nothing, and a finding it
would have settled reads as unchecked.

Do not record test files. Redline holds test context back.

Be frugal. One or two records per question, and none at all for a question
whose answer is simply that nothing turned up. Every line you record is a line
the ruling has to read.

You have %d turns, and the last of them is for filing rather than searching.
Every tool call in one turn runs before you see any result, at one turn's
cost, so run the lookups for several questions in the same turn: the greps
for each precedent question, the caller query for each caller question.
Record as soon as a lookup settles a range rather than at the end.

When you have worked through the questions, call done.

Your tools: %s.`, turns, strings.Join(tools, ", "))
}

// answerBrief is the user turn: the questions, and the diff they were asked
// about. The diff is here because a question names a symbol and the ruling
// needs the same symbol found in the same place; without it the scout would be
// grepping for a name with no idea which of its uses is the changed one.
//
// What the author said the change is for is deliberately not here, though the
// exploring brief carries it. There it earns its place by naming a plan or a
// package to go and look at, which is a lead the diff does not hold. Here
// every question already says what to look up, so the account adds no lead
// and does add a claim the author has a stake in: "this is intentional, it
// mirrors the account feed" is the sentence that turns a neutral search into
// one hunting for precedent that justifies. This stage is the one that is
// supposed to be independent of it. The ruling still reads it, in the review
// prompt it shares as its cache prefix.
func answerBrief(opts Options) string {
	var b strings.Builder
	b.WriteString("Questions to answer. Each one belongs to a finding a reviewer made ")
	b.WriteString("about this change, and the id is what to tag your records with.\n\n")
	for _, q := range opts.Questions {
		fmt.Fprintf(&b, "[%s] %s\n", q.ID, q.Kind)
		if loc := location(q); loc != "" {
			fmt.Fprintf(&b, "  about: %s\n", loc)
		}
		if q.Claim != "" {
			fmt.Fprintf(&b, "  the finding claims: %s\n", oneLine(q.Claim))
		}
		if q.Ask != "" {
			fmt.Fprintf(&b, "  question: %s\n", oneLine(q.Ask))
		}
		if q.Subject != "" {
			fmt.Fprintf(&b, "  look up: %s\n", q.Subject)
		}
		b.WriteString("\n")
	}
	b.WriteString(coveredBrief(opts))
	if asksAboutRules(opts.Questions) {
		// A rule question is answered from the same files the exploring
		// brief already inlines. Without them here the scout spends its
		// first turn on list_docs and a read of AGENTS.md to reach lines
		// the brief could have carried for a few hundred tokens.
		b.WriteString(guidelineBrief(opts.Root, guidelines(opts.Root, opts.Changed), inlineGuidelineLines, totalGuidelineLines))
	}
	b.WriteString("\nThe change these were written about:\n\n")
	b.WriteString(opts.Diff)
	return b.String()
}

func asksAboutRules(qs []Question) bool {
	for _, q := range qs {
		if strings.EqualFold(strings.TrimSpace(q.Kind), "rule") {
			return true
		}
	}
	return false
}

func location(q Question) string {
	if q.File == "" {
		return ""
	}
	if q.Line > 0 {
		return fmt.Sprintf("%s:%d", q.File, q.Line)
	}
	return q.File
}

// oneLine flattens a claim so one question is one block. A body pasted with
// its own newlines runs into the next field and the list stops parsing by eye.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// answeringPromptFragment replaces the exploring one when the envelope was
// filled by answering questions. The reviewer that reads it next is ruling
// rather than reviewing, and what it most needs to know is that an absent
// answer is not a negative one.
const answeringPromptFragment = `The context tagged scout was gathered to check
particular findings. A lookup pass was given each finding and the lookup its
author said would settle it, and it went and ran them. Every block is the real
file, copied from the repository at the revision under review, so the code is
exactly what is there. What is a judgement is the selection: which lines it
thought answered the question. Each block says which finding it was fetched
for.

Its notes say what it looked for and could not establish. Read those as
carefully as the blocks. "No precedent found for X, searched the whole tree" is
an answer and a strong one. A question with neither a block nor a note is one
nobody managed to check, which is not the same as one that came back negative,
and a finding resting on it has not been verified by anything.`

// promptFor, briefFor and fragmentFor are the three places the two jobs
// differ. Everything else in the loop is the same code doing the same thing,
// which is the argument for answering being a mode of the scout rather than a
// second program: the tools, the governor, the record validation and the rule
// that the model never writes the content are all worth having once.
func promptFor(opts Options, tools []string) string {
	if len(opts.Questions) > 0 {
		return answerPrompt(tools, opts.MaxTurns)
	}
	return systemPrompt(tools, opts.MaxTurns)
}

func briefFor(opts Options) string {
	if len(opts.Questions) > 0 {
		return answerBrief(opts)
	}
	return brief(opts)
}

// The closing brief is the last thing the scout is told. The turns are spent,
// and a model that keeps searching now has its work discarded: nothing
// reaches the review except through record, so the loop asks for the filing
// rather than stopping mid-lookup and reporting that it found nothing. Named
// separately from the notes because the model is told this, not the reader.
//
// One per job. The answering one talks about questions and a ruling, and an
// exploring scout told that has been handed a brief about a stage it is not
// in.
const (
	closingBrief = `That is the last of the turns. Look nothing else up: the searching is over.

File what you have now. Call record for every location that bears on a
question, even a partial answer, and say in the note what is still missing.
Then call done, and put what you could not establish in its notes.

Anything you do not record is lost. The findings are ruled on with whatever
is filed here, and a question with nothing against it reads as a question
nobody could answer.`

	exploringClosingBrief = `That is the last of the turns. Look nothing else up: the searching is over.

File what you have now. Call record for every location you have already read
that the reviewer needs and the diff does not show, then call done, and put
what you went looking for and did not reach in its notes.

Anything you do not record is lost. The reviewer sees the diff and what is
filed here, and a gap nobody names reads exactly like a gap that is not there.`
)

func closingFor(opts Options) string {
	if len(opts.Questions) > 0 {
		return closingBrief
	}
	return exploringClosingBrief
}

func fragmentFor(opts Options) string {
	if len(opts.Questions) > 0 {
		return answeringPromptFragment
	}
	return promptFragment
}
