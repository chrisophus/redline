package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/findings"
)

// The verifying pass: a second call that tries to refute what the first one
// found, with the answers to its own questions in front of it.
//
// Why a second call rather than a better first one. The reviewer is one turn
// with no tools over a fixed payload, which is what makes it cheap and
// replayable, and it is also why it cannot check a claim about the rest of the
// repository. Field use produced reviews whose every finding the reader
// dismissed, most of them accurate observations about code the team had
// deliberately written that way. The reader checked in seconds, because the
// reader had the repository open. This pass gives the ruling the same view.
//
// Why refutation rather than a confidence score. A model asked how sure it is
// will tell you, and it will be wrong in the direction that costs most: the
// invented finding is the confident one, because invention comes from a story
// that hangs together. A model asked to find the line that disproves a claim
// either finds it or does not, and either way the answer is checkable.
//
// The cost is one call over the same prefix as the first, which prompt caching
// serves at a fraction of the input rate, plus the answers. What it buys is a
// review that posts only what it can point at.

// Candidate is one stage-one finding with the id the later stages address it
// by. Short and positional, because a model has to echo it back and a
// fingerprint is a NUL-separated string it cannot type.
type Candidate struct {
	ID      string
	Comment findings.ReviewComment
}

// Candidates numbers a review's comments for the stages that follow.
func Candidates(rev findings.Review) []Candidate {
	out := make([]Candidate, 0, len(rev.Comments))
	for i, c := range rev.Comments {
		out = append(out, Candidate{ID: fmt.Sprintf("c%d", i+1), Comment: c})
	}
	return out
}

// Question is one lookup a finding needs, addressed to whatever runs stage
// two. It mirrors what the scout takes rather than importing it: the scout
// already imports this package for its pricing, so the dependency has to run
// this way, and the command is where the two meet.
type Question struct {
	ID      string
	Kind    string
	Ask     string
	Subject string
	Claim   string
	File    string
	Line    int
}

// Answerer is stage two. It is injected because the scout imports this package
// and cannot be imported back, and because a caller with no API key, or a
// session whose tree is gone, has to be able to leave it out and still get a
// ruling over the answers it does have, which is none.
type Answerer func(ctx context.Context, qs []Question) (*envelope.Envelope, error)

// QuestionsFor is the answerable half of a review's questions. A finding the
// diff already settles needs no lookup, and one nothing can settle gets none.
func QuestionsFor(rev findings.Review) []Question {
	return questionsFor(Candidates(rev))
}

// questionsFor is QuestionsFor over an explicit candidate set, so the verifying
// pass can look up only the findings it has not already settled.
func questionsFor(cands []Candidate) []Question {
	var out []Question
	for _, c := range cands {
		q := c.Comment.Question
		if !q.Answerable() {
			continue
		}
		out = append(out, Question{
			ID: c.ID, Kind: string(q.Kind), Ask: q.Ask, Subject: q.Subject,
			Claim: c.Comment.Body, File: c.Comment.File, Line: c.Comment.Line,
		})
	}
	return out
}

// rulePrompt is the verifying pass's own instruction, concatenated after the
// system prompt so the two calls share a prefix and the second is served from
// cache.
//
// The pass is addressed as a judge of someone else's findings, not as the
// reviewer checking its own. Sharing the prefix needs the same bytes ahead of
// it, not the same speaker, and a model told these are its findings is being
// asked to disown its own work: measured on a real run, it kept six of six
// with no answers in front of it at all.
//
// The hard part is the asymmetry. A pass rewarded only for withdrawing
// findings will withdraw everything, score perfectly on precision, and be
// worthless, and a reader with a stake in the code does exactly that. So the
// instruction names the failure in both directions and gives the withdrawal
// a burden it must meet: quote what refutes it, or quote the finding's own
// false premise. The second ground is there because a finding that is wrong
// about the language has no line in the repository that refutes it, and
// without it such a finding could only be kept.
const rulePrompt = `

A reviewer proposed findings on this change. You rule on them, before any of
them reaches the author. You did not write them, and you owe them nothing.

Below the findings are the answers to the questions the reviewer asked about
them: a cheap model ran each lookup and recorded what it found, as real lines
from the repository. Rule on every finding, using one of five verdicts.

- kept: the evidence supports it. Quote the line it rests on. For a finding
  whose question said the diff settles it, that means the lines of the diff
  it points at: re-read them, and keep it only if the claim follows from what
  they say. A finding kept on the diff alone is kept on those lines and
  nothing else.
- withdrawn: it is wrong, and you can say why. Either the evidence refutes it,
  and you quote the line that does; or it rests on a claim about the
  language, a library, or the toolchain that is false, and you quote the
  finding's own words and say what is wrong with them. The second is the
  finding no lookup can refute, because the repository has no line that says
  what the language does, and it is the one that costs the author the most.
- justified: it is true, and this repository does it on purpose. Precedent in
  code counts: when the answers show the same choice made elsewhere in code
  this change did not touch, that is this team's convention whether or not
  anyone wrote it down. So does an author saying so on an earlier review, if
  you were shown one.
- unverifiable: the lookup came back with nothing either way, so nobody knows.
  A finding whose question needed a lookup, when no lookup ran or none came
  back, is unverifiable: with no answers in front of you, you do not get to
  keep it on the reviewer's word.
- already-raised: this pull request has already heard it. Only when you were
  shown the earlier comment.

Only a kept finding reaches the author. The other four are recorded on the
report with your reason and are not posted, so they cost the author nothing
and cost you nothing to admit.

For each finding, reason before you rule. Write the analysis first: work
through what the answers show for that finding, then give the verdict it leads
to. The analysis is your thinking, not a restatement of the finding.

Two ways to fail here, and they are not symmetric in how they feel.

Withdrawing everything is the easy one and it is worthless. A pass that keeps
nothing has perfect precision and no value, and the author learns to skip the
tool entirely. Withdraw a finding when you can say what is wrong with it. "I
am not sure any more" is unverifiable, not withdrawn.

Keeping everything is the other, and it is what put you here. A finding you
cannot point at is a finding the author will dismiss, and the first one they
dismiss is the last one they read carefully.

An answer that did not come back is not a negative answer. A question whose
lookup found nothing at all is unverifiable. A lookup that found thirty places
doing the flagged thing is justified, and that is a real answer, not a missing
one. Read the notes as carefully as the code: "no precedent found for X,
searched the whole tree" is evidence and says so.

Two findings that ask the same question about the same thing are one finding.
Keep the clearer one and rule the other already-raised.

Do not write new findings here. You are ruling on the ones you have. If the
answers show you something the reviewer missed entirely, that is a real cost
of this design and it is worth less than the noise it prevents.`

// ruleSchema is the ruling's output contract. Every property required, for the
// reason the review's own schema gives: a model that must emit a field cannot
// quietly drop the one carrying the evidence.
func ruleSchema() map[string]any {
	ruling := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"analysis", "finding", "verdict", "evidence", "why"},
		"properties": map[string]any{
			"analysis": map[string]any{
				"type": "string",
				"description": "Work through what the answers show for this finding here, " +
					"before the verdict. Your reasoning, not a restatement of the finding.",
			},
			"finding": map[string]any{
				"type":        "string",
				"description": "The id of the finding, exactly as given in brackets.",
			},
			"verdict": map[string]any{
				"type": "string",
				"enum": []string{
					findings.VerifiedKept, findings.VerifiedWithdrawn,
					findings.VerifiedJustified, findings.VerifiedUnverifiable,
					findings.VerifiedAlreadyRaised,
				},
			},
			"evidence": map[string]any{
				"type": "string",
				"description": "The line this rests on, quoted from the material, with where it " +
					"came from. Empty only when the verdict is unverifiable.",
			},
			"why": map[string]any{
				"type":        "string",
				"description": "One sentence on what the evidence shows.",
			},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"rulings"},
		"properties": map[string]any{
			"rulings": map[string]any{"type": "array", "items": ruling},
		},
	}
}

// rulingItem is one ruling as it comes off the wire. analysis leads so the
// model reasons before it commits to a verdict rather than justifying one it
// has already written.
type rulingItem struct {
	Analysis string `json:"analysis"`
	Finding  string `json:"finding"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	Why      string `json:"why"`
}

// ruleWire is what comes back. rulings is a RawMessage because a gateway that
// serves one vendor's model over another's protocol does not always honour the
// json_schema, and returns the array wrapped in a JSON string rather than as
// the array itself. parseRulings reads both shapes.
type ruleWire struct {
	Rulings json.RawMessage `json:"rulings"`
}

// candidatesSection puts the findings in front of the pass that must rule on
// them, each with the id it answers to and the question it asked.
func candidatesSection(cands []Candidate) string {
	var b strings.Builder
	b.WriteString("## The findings to rule on\n\n")
	for _, c := range cands {
		loc := c.Comment.File
		if c.Comment.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.Comment.File, c.Comment.Line)
		}
		fmt.Fprintf(&b, "- [%s] %s\n  %s\n", c.ID, loc, oneLine(c.Comment.Body))
		q := c.Comment.Question
		switch {
		case q.Kind == findings.QuestionDiff:
			b.WriteString("  you said the material already shown settles this\n")
		case q.Kind == findings.QuestionNone:
			b.WriteString("  you said nothing available would settle this\n")
		case q.Ask != "" || q.Subject != "":
			fmt.Fprintf(&b, "  you asked (%s): %s", q.Kind, oneLine(q.Ask))
			if q.Subject != "" {
				fmt.Fprintf(&b, " [%s]", q.Subject)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// answersSection renders what stage two found, grouped by the finding it was
// fetched for. An expansion with no tag is still shown: the scout may have
// found something worth reading without saying which claim it bears on, and
// dropping it would throw away evidence over bookkeeping.
func answersSection(env *envelope.Envelope) string {
	if env == nil || (len(env.Expansions) == 0 && len(env.Notes) == 0) {
		return "## The answers\n\nNothing was looked up for these findings. " +
			"No finding here has been checked against the repository, so any " +
			"that needed a lookup is unverifiable rather than confirmed.\n\n"
	}
	byID := map[string][]envelope.Expansion{}
	var order []string
	for _, x := range env.Expansions {
		id := x.Details["answers"]
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = append(byID[id], x)
	}
	sort.Strings(order)

	var b strings.Builder
	b.WriteString("## The answers\n\n")
	b.WriteString("What the lookups found, copied from the repository at the revision under review.\n\n")
	for _, id := range order {
		if id == "" {
			b.WriteString("### Found, but not tied to a particular finding\n\n")
		} else {
			fmt.Fprintf(&b, "### For [%s]\n\n", id)
		}
		for _, x := range byID[id] {
			loc := x.File
			if x.StartLine > 0 {
				loc = fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
			}
			fmt.Fprintf(&b, "%s · %s · %s\n```\n%s\n```\n\n",
				x.Role, x.Symbol, loc, strings.TrimRight(x.Content, "\n"))
		}
	}
	if len(env.Notes) > 0 {
		b.WriteString("### What the lookups could not establish\n\n")
		b.WriteString("These are answers too. A search that came back empty is evidence; " +
			"a question nobody reached is not.\n\n")
		for _, n := range env.Notes {
			b.WriteString("- " + n + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Apply writes the rulings onto the review.
//
// A comment nobody ruled on keeps its own confidence and posts, which is what
// makes the whole pass optional: a run with no API key for stage two, or a
// session whose tree has gone, produces the review it always did rather than
// an empty one.
func Apply(rev findings.Review, cands []Candidate, rulings map[string]findings.Ruling) findings.Review {
	out := rev
	out.Comments = make([]findings.ReviewComment, len(rev.Comments))
	copy(out.Comments, rev.Comments)
	for i, c := range cands {
		if i >= len(out.Comments) {
			break
		}
		r, ok := rulings[c.ID]
		if !ok {
			continue
		}
		r.Verdict = findings.NormalizeVerdict(r.Verdict)
		out.Comments[i].Ruling = r
		if !r.Posts() {
			// The one rule about what reaches an author, spelled the way the
			// report and post already spell it.
			out.Comments[i].Confidence = findings.ConfidenceLow
		}
	}
	return out
}

// Kept counts what survived, for the line the command prints. A reader who is
// told a review found nine things and sees two wants to know the other seven
// were checked rather than lost.
func Kept(rev findings.Review) (kept, ruled int) {
	for _, c := range rev.Comments {
		if c.Ruling.Verdict != "" {
			ruled++
		}
		if c.Ruling.Posts() {
			kept++
		}
	}
	return kept, ruled
}

// Verify runs the two stages after the review: the lookups a finding asked
// for, then the ruling over what they found.
//
// It degrades rather than fails, at every step. No answerer, no API key, a
// session whose tree has gone, a lookup that errors, a ruling call that comes
// back unparseable: each of those leaves the review exactly as stage one wrote
// it, and says so. A verifying pass that could take the whole review down with
// it would be a worse trade than the noise it removes.
func Verify(ctx context.Context, in Input, opts Options, stageOne *Result) (*Result, error) {
	opts = opts.withDefaults()
	cands := Candidates(stageOne.Review)
	if len(cands) == 0 {
		// Nothing to rule on. A clean review is the answer this whole design
		// is trying to make possible, so it must not cost a second call.
		return stageOne, nil
	}

	// Matched before anything is looked up or sent. A finding this pull
	// request has already heard and had answered needs neither a lookup nor a
	// ruling, and settling that first is what stops the scout spending its
	// loop on a question whose finding is about to be discarded.
	settled := alreadyRaised(cands, in.Prior)
	pending := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if _, done := settled[c.ID]; !done {
			pending = append(pending, c)
		}
	}
	if len(pending) == 0 {
		// Every finding on this change has already been raised and answered.
		// There is nothing left for a ruling to decide, and paying for a call
		// to be told so is the exact waste the second field round paid for.
		stageOne.Verified = true
		stageOne.Review = Apply(stageOne.Review, cands, settled)
		return stageOne, nil
	}

	var answers *envelope.Envelope
	var qs []Question
	if opts.Answer != nil {
		qs = questionsFor(pending)
		if len(qs) > 0 {
			env, err := opts.Answer(ctx, qs)
			if err != nil {
				// Named, not swallowed. A ruling made over no answers is a
				// ruling made from the same material that produced the claim,
				// which is the thing this stage exists to stop being the whole
				// of it.
				if opts.Progress != nil {
					opts.Progress(fmt.Sprintf("the lookups failed, so the ruling has nothing to check against: %v", err))
				}
			} else {
				answers = env
			}
		}
	}
	// Questions were asked and nothing came back for any of them: the scout
	// failed, or it ran and filed neither a record nor a note. The ruling is
	// not sent. It would be ruling on the same material that produced the
	// claims, and a finding whose question named a lookup is unverifiable
	// when no lookup ran, so every one of them would be withheld in a single
	// step and the author would get an empty review that reads exactly like
	// a clean change. The findings go out as the review wrote them, with the
	// reason on the report, which is what the tripwire branch below does for
	// the same reason.
	//
	// A note with no records is an answer: "no precedent found for X,
	// searched the whole tree" is evidence, and the ruling is sent.
	if len(qs) > 0 && !answered(answers) {
		stageOne.Verified = true
		stageOne.VerifyFailed = fmt.Sprintf(
			"the %d lookup(s) the review asked for came back with nothing, so the findings below are as the review wrote them",
			len(qs))
		return stageOne, nil
	}

	res := stageOne.ruleRequest(in, opts, pending, answers)
	if opts.DryRun {
		return res, nil
	}
	// The tripwire measures the ruling call too, not only stage one. The
	// ruling shares stage one's prefix but adds every answer the scout
	// recorded, and a scout envelope is the one input here a small change
	// cannot keep small. A ruling whose worst case is over the cap is not
	// sent, and the review is left checked-but-unruled rather than billed for
	// a call nobody agreed to.
	if res.CostKnown && res.CostCeilingUSD > opts.MaxCostUSD {
		stageOne.Verified = true
		stageOne.VerifyFailed = fmt.Sprintf(
			"the ruling's worst-case cost %s is over the %s tripwire, so the findings below are as the review wrote them",
			FormatCost(res.CostCeilingUSD, true), FormatCost(opts.MaxCostUSD, true))
		return stageOne, nil
	}
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("ruling on %d finding(s)", len(pending)))
	}
	out, err := runOnce(ctx, in, opts, res)
	// The pass was paid for whichever way it ended, so its usage folds into
	// the review's before anything else happens. The ruling's output is kept
	// out of the review's own count: it is a handful of short objects, and
	// folding it into the output the ledger takes a median of would inflate
	// the estimate every later review is priced against. Its cost is added
	// back on, because it was billed all the same.
	stageOne.Usage.InputTokens += out.Usage.InputTokens
	stageOne.Usage.CacheReadTokens += out.Usage.CacheReadTokens
	stageOne.Usage.CacheWriteTokens += out.Usage.CacheWriteTokens
	stageOne.RulingOutputTokens += out.Usage.OutputTokens
	stageOne.Duration += out.Duration
	stageOne.CostUSD, stageOne.CostKnown = stageOne.Usage.Cost(opts.Model)
	if rc, ok := (Usage{OutputTokens: stageOne.RulingOutputTokens}).Cost(opts.Model); ok && stageOne.CostKnown {
		stageOne.CostUSD += rc
	}
	stageOne.Verified = true
	if err != nil {
		stageOne.VerifyFailed = err.Error()
		return stageOne, nil
	}
	// The pass completed, so from here a finding is posted only if a ruling
	// kept it.
	rulings := combineRulings(cands, pending, settled, out.Rulings,
		verifyCorpus(stageOne.Prompt, answers))
	stageOne.Review = Apply(stageOne.Review, cands, rulings)
	return stageOne, nil
}

// answered reports whether the lookups produced anything a ruling could read.
// An envelope with no expansions and no notes is a search that filed nothing,
// which is not the same as a search that looked and said so: the note is the
// answer in that case, and the prompt tells the model to read the notes as
// carefully as the code.
func answered(env *envelope.Envelope) bool {
	return env != nil && (len(env.Expansions) > 0 || len(env.Notes) > 0)
}

// ruleRequest assembles the second call. The prefix is stage one's own, which
// is what makes this cheap: the system block and the user turn up to the
// findings are byte-identical, so the endpoint serves them from cache.
func (r *Result) ruleRequest(in Input, opts Options, cands []Candidate, answers *envelope.Envelope) *Result {
	out := r.clone()
	// The system block is left byte-identical to stage one and the ruling
	// instruction goes at the tail of the user turn, beside the findings it
	// refers to. This once bought a prompt-cache hit as well, until the wire
	// showed the schema in front of the cached prefix: a ruling sends its own
	// schema, so it never reads the review's entry however the two prompts are
	// arranged. What is left is the reason that still holds - a judge of these
	// findings works under the rules the review was given.
	out.System = r.System
	out.Prompt = r.Prompt + "\n" + candidatesSection(cands) +
		boundAnswers(answersSection(answers)) + rulePrompt
	out.Schema = ruleSchema()
	out.InputEstimate = envelope.EstimateTokens(out.System) + envelope.EstimateTokens(out.Prompt)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedRulingTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
}

// maxAnswersBytes bounds the scout's answers where they are stitched into the
// ruling prompt. The scout runs under its own token governor, but a governor
// on turns is not a bound on bytes, and the answers are the one input to the
// ruling that a small change cannot keep small. Past this the tripwire in
// Verify would refuse the call anyway; truncating here keeps a large but
// payable ruling from carrying a tail nobody will read.
const maxAnswersBytes = 200_000

func boundAnswers(s string) string {
	if len(s) <= maxAnswersBytes {
		return s
	}
	// Cut on a rune boundary so the prompt stays valid UTF-8.
	cut := maxAnswersBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n\n… the answers were truncated here to stay within budget; " +
		"a finding whose evidence fell past this point reads as unverifiable.\n\n"
}

// combineRulings is the ruling map Apply writes after a completed pass. The
// model's rulings are sanitized against the evidence; a pending candidate the
// model left out fails closed to unverifiable, so a truncated or lazy response
// cannot post a finding nobody checked; and the deterministic already-raised
// wins over the model's, because it read the actual thread rather than guessed
// from wording.
func combineRulings(cands, pending []Candidate, settled, model map[string]findings.Ruling, corpus string) map[string]findings.Ruling {
	rulings := sanitizeRulings(cands, model, corpus)
	for _, c := range pending {
		if _, ok := rulings[c.ID]; !ok {
			rulings[c.ID] = unverifiable(
				"the checking pass returned no ruling for this finding, so it stays unverified and is not posted")
		}
	}
	for id, r := range settled {
		rulings[id] = r
	}
	return rulings
}

// sanitizeRulings holds the model's rulings to the evidence standard the
// prompt sets but the schema cannot. A ruling that suppresses a finding has a
// burden of proof, and a model under pressure to be decisive will meet it in
// words rather than in the material. Both checks demote to unverifiable, which
// folds the finding rather than posting it or claiming it settled:
//
//   - already-raised is a claim about history. The deterministic matcher owns
//     the real ones; the only honest model already-raised left is a duplicate
//     of another finding that asks the same question, which the prompt asks it
//     to collapse. Anything else suppresses a finding on a thread nobody can
//     find.
//   - withdrawn and justified rest on a quoted line. If no run of that quote
//     is in what the ruling was shown, the quote was invented, and a finding
//     withdrawn or excused on invented evidence is the exact failure this pass
//     exists to prevent.
func sanitizeRulings(cands []Candidate, rulings map[string]findings.Ruling, corpus string) map[string]findings.Ruling {
	byID := make(map[string]Candidate, len(cands))
	shared := map[string]int{}
	for _, c := range cands {
		byID[c.ID] = c
		if k := c.Comment.Question.Key(); k != "" {
			shared[k]++
		}
	}
	out := make(map[string]findings.Ruling, len(rulings))
	for id, r := range rulings {
		r.Verdict = findings.NormalizeVerdict(r.Verdict)
		switch r.Verdict {
		case findings.VerifiedAlreadyRaised:
			c, ok := byID[id]
			if !ok || c.Comment.Question.Key() == "" || shared[c.Comment.Question.Key()] < 2 {
				r = unverifiable("the checking pass called this already raised, but this pull request " +
					"carries no earlier thread for it and no other finding asks the same question")
			}
		case findings.VerifiedWithdrawn, findings.VerifiedJustified:
			if !groundedEvidence(r.Evidence, corpus) {
				r = unverifiable("the checking pass gave evidence that is not in the material it was shown, " +
					"so the verdict rests on nothing checkable")
			}
		}
		out[id] = r
	}
	return out
}

func unverifiable(why string) findings.Ruling {
	return findings.Ruling{Verdict: findings.VerifiedUnverifiable, Why: why}
}

// minEvidenceRun is the shortest quote taken as real. Long enough that a run
// of it turning up in the material is a line the ruling read rather than a
// coincidence, short enough that one quoted statement clears it.
const minEvidenceRun = 24

// groundedEvidence reports whether a run of the quoted evidence appears in the
// material the ruling was shown. Case and whitespace are forgiven, because a
// model requotes a line reflowed and recapitalised; nothing else is, because
// the point is that the words were there to quote.
func groundedEvidence(evidence, corpus string) bool {
	e := collapseSpace(strings.ToLower(evidence))
	if e == "" {
		return false
	}
	c := collapseSpace(strings.ToLower(corpus))
	if len(e) <= minEvidenceRun {
		return strings.Contains(c, e)
	}
	for i := 0; i+minEvidenceRun <= len(e); i++ {
		if strings.Contains(c, e[i:i+minEvidenceRun]) {
			return true
		}
	}
	return false
}

// collapseSpace reduces every run of whitespace to one space, so a quote that
// was reflowed still matches the source it came from.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// verifyCorpus is everything the ruling was shown that could carry evidence:
// the review's own prompt, which holds the diff, the priors and the pull
// request history, and the answers the scout recorded. The candidates section
// is left out on purpose, so a finding cannot count its own wording as the
// evidence that clears it.
func verifyCorpus(prompt string, answers *envelope.Envelope) string {
	return prompt + "\n" + answersSection(answers)
}

// ExpectedRulingTokens prices the second call. A ruling is a handful of short
// objects rather than a review's prose, so it is a fraction of the first
// call's output and pricing it at the first call's median would quote a
// number nobody will pay.
const ExpectedRulingTokens int64 = 2000

// rulesRatherThanReviews reports which contract this request went out under.
// The schema is the honest place to ask: it is what the endpoint was told to
// constrain the answer to, so it cannot disagree with what came back.
func (r *Result) rulesRatherThanReviews() bool {
	if r == nil || r.Schema == nil {
		return false
	}
	props, _ := r.Schema["properties"].(map[string]any)
	_, ok := props["rulings"]
	return ok
}

// parseRulings reads the verifying pass's response.
//
// A ruling naming no finding, or naming one twice, is dropped rather than
// failing the pass. The findings are the review and one unattached ruling is
// worth losing on its own; a comment nobody ruled on keeps its own confidence
// and posts, which is the safe direction.
func parseRulings(body []byte) (map[string]findings.Ruling, error) {
	var w ruleWire
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("the verifying pass's response did not parse: %w", err)
	}
	items, err := decodeRulingItems(w.Rulings)
	if err != nil {
		return nil, fmt.Errorf("the verifying pass's response did not parse: %w", err)
	}
	out := make(map[string]findings.Ruling, len(items))
	for _, r := range items {
		id := findingID(r.Finding)
		if id == "" {
			continue
		}
		if _, dup := out[id]; dup {
			continue
		}
		out[id] = findings.Ruling{
			Verdict:  findings.NormalizeVerdict(r.Verdict),
			Evidence: strings.TrimSpace(r.Evidence),
			Why:      strings.TrimSpace(r.Why),
			Analysis: strings.TrimSpace(r.Analysis),
		}
	}
	return out, nil
}

// findingID reads the id out of whatever the model put in the finding field.
// The candidates are shown as "[c1] file:line", and a model at high effort
// copied that whole string back. Trimming brackets off the ends left
// "c1] file:line", nothing matched, and every finding was recorded as
// unruled and not posted, which the summary reported as none kept. The id is
// the first token, with or without its brackets.
func findingID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	if i := strings.IndexAny(s, "] \t\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// decodeRulingItems reads the rulings out of whatever shape the wire returned,
// including a stringified object whose own text lost a level of escaping.
//
// decodeRulingShapes reads the shapes that are still well-formed JSON. When
// none of them parse, a gateway that returned the whole object as a JSON string
// and escaped it only once is the remaining case: the decoded value is not
// valid JSON, because a quote inside a field's text sits raw where the wire
// should carry an escaped one. relaxUnescapedQuotes re-escapes those and the
// shapes are tried one more time. It runs only after the strict read has
// failed, so a well-formed response never reaches it.
func decodeRulingItems(raw json.RawMessage) ([]rulingItem, error) {
	items, err := decodeRulingShapes(raw)
	if err == nil {
		return items, nil
	}
	if repaired, ok := relaxUnescapedQuotes(raw); ok {
		if items, err2 := decodeRulingShapes(repaired); err2 == nil {
			return items, nil
		}
	}
	return nil, err
}

// decodeRulingShapes reads the rulings out of the shapes that parse as JSON.
//
// The forced tool call on the OpenAI wire fixed this at the source, but the
// review still posts over gateways that drop the schema, so the parse stays
// tolerant. The array is the shape the schema names. A gateway that dropped
// the schema has been seen to stringify the whole value, and to return an
// object instead of an array: one ruling on its own, or a map keyed by the
// finding id. Each of those carries the same rulings, so they are read out
// rather than failing the pass open.
func decodeRulingShapes(raw json.RawMessage) ([]rulingItem, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var items []rulingItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	// Stringified: unwrap once and read whatever the string held.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil, nil
		}
		return decodeRulingItems(json.RawMessage(s))
	}
	if raw[0] == '{' {
		// A re-wrapped ruleWire, {"rulings": …} nested one level down, which a
		// gateway has produced by stringifying the whole object into the field.
		// Unwrap it before trying the bare-object shapes, or the wrapper reads
		// as a ruling with no fields.
		var w ruleWire
		if json.Unmarshal(raw, &w) == nil && len(bytes.TrimSpace(w.Rulings)) > 0 {
			return decodeRulingItems(w.Rulings)
		}
		if items, ok := rulingItemsFromObject(raw); ok {
			return items, nil
		}
	}
	return nil, fmt.Errorf("rulings is neither an array nor an object of rulings: %s", snippet(raw))
}

// relaxUnescapedQuotes re-escapes the raw double-quotes a gateway leaves in a
// stringified ruling's own text. A gateway that returns the whole object as a
// JSON string and escapes it only once produces valid outer JSON whose decoded
// value is not valid JSON: every structural quote is intact, but a quote inside
// a field (a snippet like == "") sits raw where the wire should carry an
// escaped one, and the strict shapes stop at it. This walks the bytes and
// escapes any in-string quote that is not a real terminator, judged by whether
// the next non-space byte continues the structure (':', ',', '}', ']', or the
// end). Reported false when nothing was re-escaped, so a payload it cannot help
// falls back to the original parse error rather than a second identical one.
func relaxUnescapedQuotes(raw json.RawMessage) (json.RawMessage, bool) {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || (s[0] != '{' && s[0] != '[') {
		return nil, false
	}
	var b bytes.Buffer
	b.Grow(len(s) + 8)
	inStr := false
	changed := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			b.WriteByte(c)
			if c == '"' {
				inStr = true
			}
			continue
		}
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if c == '"' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
				j++
			}
			if j >= len(s) || s[j] == ':' || s[j] == ',' || s[j] == '}' || s[j] == ']' {
				b.WriteByte('"')
				inStr = false
			} else {
				b.WriteString(`\"`)
				changed = true
			}
			continue
		}
		b.WriteByte(c)
	}
	if !changed {
		return nil, false
	}
	return json.RawMessage(b.Bytes()), true
}

// rulingItemsFromObject reads the two object shapes a schema-dropping gateway
// returns: a single ruling, or a map from finding id to the rest of it.
func rulingItemsFromObject(raw json.RawMessage) ([]rulingItem, bool) {
	var one rulingItem
	if json.Unmarshal(raw, &one) == nil && (one.Finding != "" || one.Verdict != "") {
		return []rulingItem{one}, true
	}
	var m map[string]rulingItem
	if json.Unmarshal(raw, &m) == nil && len(m) > 0 {
		out := make([]rulingItem, 0, len(m))
		for id, it := range m {
			if it.Finding == "" {
				it.Finding = id
			}
			out = append(out, it)
		}
		return out, true
	}
	return nil, false
}

// snippet bounds a raw body for an error message, so a parse failure shows the
// shape that broke it without pasting a whole response into the log.
func snippet(raw []byte) string {
	const max = 200
	s := strings.TrimSpace(string(raw))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// alreadyRaised matches candidates against what this pull request has already
// heard, before the ruling call is made.
//
// The second field round is why this is deterministic rather than left to the
// model. The same pull request was reviewed twice on an unchanged head, after
// the author had replied on every thread, and the second review posted two of
// the same defects again under new wording on new lines: the pre-transaction
// race came back as "CopyFrom cannot ON CONFLICT", the checkpoint ordinal came
// back from a different function. Stage zero had shown the earlier threads and
// their replies. A ruling asked whether the pull request had heard this exact
// comment could answer no and be telling the truth.
//
// So the match is on the question, which is the same sentence for both
// wordings, and it is applied before the model sees the candidates. Two
// fallbacks for a thread posted before the marker existed: the same file with
// a message that normalises to the same thing, which is the fingerprint's own
// test, and nothing else. A looser fallback would suppress real findings on a
// busy file, and the model still has the thread text to catch what this misses.
//
// Only threads somebody answered. An unanswered thread means the author has
// not looked, and saying it once more where they are looking is not noise.
func alreadyRaised(cands []Candidate, prior []feedback.Thread) map[string]findings.Ruling {
	if len(prior) == 0 {
		return nil
	}
	byQuestion := map[string]feedback.Thread{}
	byMessage := map[string]feedback.Thread{}
	for _, t := range prior {
		if !t.Answered() {
			continue
		}
		if t.Question != "" {
			byQuestion[t.Question] = t
		}
		byMessage[messageKey(t.File, t.Said)] = t
	}

	out := map[string]findings.Ruling{}
	for _, c := range cands {
		t, ok := byQuestion[c.Comment.Question.Key()]
		if !ok || c.Comment.Question.Key() == "" {
			t, ok = byMessage[messageKey(c.Comment.File, c.Comment.Body)]
		}
		if !ok {
			continue
		}
		out[c.ID] = findings.Ruling{
			Verdict:  findings.VerifiedAlreadyRaised,
			Evidence: threadEvidence(t),
			Why: "this pull request already carries this finding and somebody answered it; " +
				"saying it again in other words is how a reader learns to stop reading",
		}
	}
	return out
}

// messageKey is the fingerprint's own identity, file plus the message with its
// digits collapsed, which is what catches a thread posted before the question
// marker existed. It catches a requote and nothing more; the wording that
// moves is exactly what it cannot follow.
func messageKey(file, body string) string {
	return strings.ToLower(strings.TrimSpace(file)) + "\x00" +
		findings.NormalizeMessage(strings.ToLower(strings.TrimSpace(body)))
}

// threadEvidence quotes what was said back, because a finding withheld on the
// strength of an earlier answer has to show the reader that answer.
func threadEvidence(t feedback.Thread) string {
	loc := t.File
	if t.Line > 0 {
		loc = fmt.Sprintf("%s:%d", t.File, t.Line)
	}
	if len(t.Replies) > 0 {
		who := t.Replies[0].Author
		if who == "" {
			who = "someone"
		}
		return fmt.Sprintf("%s, on the earlier thread at %s: %s", who, loc, oneLine(t.Replies[0].Body))
	}
	return fmt.Sprintf("an earlier thread at %s, which somebody reacted to", loc)
}
