package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
	var out []Question
	for _, c := range Candidates(rev) {
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
// The hard part is the asymmetry. A pass rewarded only for withdrawing
// findings will withdraw everything, score perfectly on precision, and be
// worthless, and a reader with a stake in the code does exactly that. So the
// instruction names the failure in both directions and gives the withdrawal a
// burden of proof the keep does not have: refuting takes a line you can quote.
const rulePrompt = `

You have already reviewed this change. Now check what you found, before any of
it reaches the author.

Below your findings are the answers to the questions you asked about them: a
cheap model ran each lookup and recorded what it found, as real lines from the
repository. Rule on every finding, using one of five verdicts.

- kept: the evidence supports it, or it never needed evidence because what you
  were shown settles it. Quote the line it rests on.
- withdrawn: the evidence refutes it. Quote the line that does.
- justified: it is true, and this repository does it on purpose. Precedent in
  code counts: when the answers show the same choice made elsewhere in code
  this change did not touch, that is this team's convention whether or not
  anyone wrote it down. So does an author saying so on an earlier review, if
  you were shown one.
- unverifiable: the lookup came back with nothing either way, so nobody knows.
- already-raised: this pull request has already heard it. Only when you were
  shown the earlier comment.

Only a kept finding reaches the author. The other four are recorded on the
report with your reason and are not posted, so they cost the author nothing
and cost you nothing to admit.

Two ways to fail here, and they are not symmetric in how they feel.

Withdrawing everything is the easy one and it is worthless. A pass that keeps
nothing has perfect precision and no value, and the author learns to skip the
tool entirely. Withdraw a finding when you can quote what refutes it. "I am
not sure any more" is unverifiable, not withdrawn.

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
answers show you something you missed entirely, that is a real cost of this
design and it is worth less than the noise it prevents.`

// ruleSchema is the ruling's output contract. Every property required, for the
// reason the review's own schema gives: a model that must emit a field cannot
// quietly drop the one carrying the evidence.
func ruleSchema() map[string]any {
	ruling := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"finding", "verdict", "evidence", "why"},
		"properties": map[string]any{
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

// ruleWire is what comes back.
type ruleWire struct {
	Rulings []struct {
		Finding  string `json:"finding"`
		Verdict  string `json:"verdict"`
		Evidence string `json:"evidence"`
		Why      string `json:"why"`
	} `json:"rulings"`
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

	var answers *envelope.Envelope
	if opts.Answer != nil {
		qs := QuestionsFor(stageOne.Review)
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

	// Matched before the call, not by it. A model asked whether the pull
	// request has heard a claim can answer honestly about the wording it was
	// shown and miss the claim underneath; the question is the same sentence
	// either way, so this is arithmetic rather than judgement.
	settled := alreadyRaised(cands, in.Prior)

	res := stageOne.ruleRequest(in, opts, cands, answers)
	if opts.DryRun {
		return res, nil
	}
	if len(settled) == len(cands) {
		// Every finding on this change has already been raised and answered.
		// There is nothing left for a ruling to decide, and paying for a call
		// to be told so is the exact waste the second field round paid for.
		stageOne.Verified = true
		stageOne.Review = Apply(stageOne.Review, cands, settled)
		return stageOne, nil
	}
	out, err := runOnce(ctx, in, opts, res)
	// The pass was paid for whichever way it ended, so its usage folds into
	// the review's before anything else happens.
	stageOne.Usage.InputTokens += out.Usage.InputTokens
	stageOne.Usage.OutputTokens += out.Usage.OutputTokens
	stageOne.Usage.CacheReadTokens += out.Usage.CacheReadTokens
	stageOne.Usage.CacheWriteTokens += out.Usage.CacheWriteTokens
	stageOne.CostUSD, stageOne.CostKnown = stageOne.Usage.Cost(opts.Model)
	stageOne.Verified = true
	if err != nil {
		stageOne.VerifyFailed = err.Error()
		return stageOne, nil
	}
	// The deterministic match wins over the model's. It is the one that read
	// the actual thread, and a ruling that decided a re-raised finding was
	// worth keeping is exactly the failure this pass is for.
	for id, r := range settled {
		out.Rulings[id] = r
	}
	stageOne.Review = Apply(stageOne.Review, cands, out.Rulings)
	return stageOne, nil
}

// ruleRequest assembles the second call. The prefix is stage one's own, which
// is what makes this cheap: the system block and the user turn up to the
// findings are byte-identical, so the endpoint serves them from cache.
func (r *Result) ruleRequest(in Input, opts Options, cands []Candidate, answers *envelope.Envelope) *Result {
	out := r.clone()
	out.System = r.System + rulePrompt
	out.Prompt = r.Prompt + "\n" + candidatesSection(cands) + answersSection(answers)
	out.Schema = ruleSchema()
	out.InputEstimate = envelope.EstimateTokens(out.System) + envelope.EstimateTokens(out.Prompt)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedRulingTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
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
	out := make(map[string]findings.Ruling, len(w.Rulings))
	for _, r := range w.Rulings {
		id := strings.Trim(strings.TrimSpace(r.Finding), "[]")
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
		}
	}
	return out, nil
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
