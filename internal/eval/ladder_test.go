package eval_test

// TestLadder builds a reviewer up from nothing, one layer at a time, and
// scores every rung on the same fixtures with the same scorer, so the rung
// where recall dies names the layer that killed it. It exists because
// subtractive debugging of the shipped pipeline cost one paid sweep per
// hypothesis and eliminated the model, the prompt, the describing split and
// the reasoning dial without finding the cause.
//
//	rung 0  minimal instruction + the raw diff
//	rung 1  minimal instruction + the shipped packet
//	rung 2  the shipped system prompt + the shipped packet
//	shipped adds the strict tool grammar, and is TestSweep's one-shot arm
//
// Measured 2026-09-14 on claude-sonnet-5, gorefactor-changectx and
// gorefactor-nil-rules, three samples each, called directly:
//
//	rung 0  caught  9/31, 12 unlabelled
//	rung 1  caught 10/31,  4 unlabelled
//	rung 2  caught  7/31,  2 unlabelled
//
// Recall does not separate the rungs on this pair. What the packet and the
// shipped prompt measurably buy is quiet: each layer roughly halves the
// comments nobody labelled, at about the same number of catches.
//
// Every earlier figure for these rungs is void. They went through a local
// proxy that appended its own instruction block to the system prompt, one
// telling the model never to restate code, file contents or diffs, and that
// run put rung 1 at 12/31 and rung 2 at 3/31. The k=1 figures before it
// (rung 0 at 12/38 beating rung 1 at 11/38) were inside the noise as well as
// behind the proxy. A paid run has to reach the vendor's endpoint unaltered,
// or it measures somebody else's prompt.
//
// No rung carries the cohort partition, the scout's lookups or the ruling, so
// nothing here compares a rung with `--pipeline staged`. The rungs are
// unpriced, so no cost claim follows from them either.
//
// Every rung asks for free-form JSON and parses it with findings.LoadReview,
// which is the same path an agent's review.json takes, so a rung is scored
// exactly as an agent arm would be.
//
// Thinking is disabled on these calls. Left on it is the default, and on the
// larger fixtures it spent the whole 32k output budget before emitting a
// single text token, which reads as a silent model and is not one.
//
//	REDLINE_LADDER=0 REDLINE_EVAL_MODEL=claude-sonnet-5 go test ./internal/eval -run TestLadder -v

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/chrisophus/redline/internal/eval"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

const jsonContract = `Reply with one JSON object and nothing else: no prose
before it, no markdown fence, no preamble about what you are about to check.

{"overview": "one paragraph on what this change does",
 "files": [{"path": "...", "summary": "what this file's change does"}],
 "comments": [{"path": "...", "line": 0, "category": "correctness",
               "severity": "warning", "confidence": "high",
               "body": "what is wrong and what happens because of it",
               "question": {"kind": "diff", "ask": "what to check",
                            "subject": "what to look it up on"}}]}

question is an object, never a string. Omit it only if the comment needs no
check.

Keep each file summary to one sentence: the comments are what matter, and a
reply that spends its budget on summaries gets cut off before them.`

// ladderMaxTokens is the output budget every rung gets. A rung that ran under
// a different one would be measuring the ceiling rather than the material.
const ladderMaxTokens = 32000

const minimalSystem = `You are reviewing one change in a code repository.

Report every defect you can support from the material below, each as its own
comment. A change may carry several independent defects; a reviewer that
reports one and stops has failed. Do not invent findings: zero comments is
the right answer on a change that has none.

` + jsonContract

func TestLadder(t *testing.T) {
	model := os.Getenv("REDLINE_EVAL_MODEL")
	if model == "" {
		t.Skip("set REDLINE_EVAL_MODEL to run the paid ladder")
	}
	rung := os.Getenv("REDLINE_LADDER")
	if rung == "" {
		t.Skip("set REDLINE_LADDER to 0, 1 or 2")
	}

	fixtures, err := eval.Load("../../testdata/fixtures")
	if err != nil {
		t.Fatal(err)
	}
	// A comma-separated list, as TestSweep takes. One name at a time scored
	// each fixture under its own denominator, so the two-fixture probe that
	// carries most of the expectations had to be added up by hand, and a rung
	// summed that way is not comparable with one Sum pooled. An unknown name
	// fails rather than silently narrowing the set.
	if only := os.Getenv("REDLINE_EVAL_FIXTURE"); only != "" {
		want := map[string]bool{}
		for _, name := range strings.Split(only, ",") {
			want[strings.TrimSpace(name)] = true
		}
		var kept []eval.Fixture
		for _, f := range fixtures {
			if want[f.Annotation.Name] {
				kept = append(kept, f)
				delete(want, f.Annotation.Name)
			}
		}
		for name := range want {
			t.Fatalf("REDLINE_EVAL_FIXTURE names %q, which is not a fixture", name)
		}
		fixtures = kept
	}

	// Samples, for the reason TestSweep takes them: a review is not a stable
	// function of its input, and the union of one sample is close to a coin
	// flip per expectation. The rungs were measured at one sample and compared
	// against each other on a three-point spread, which is inside the noise
	// this buys its way out of.
	samples := 1
	if s := os.Getenv("REDLINE_EVAL_SAMPLES"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("REDLINE_EVAL_SAMPLES=%q is not a positive count", s)
		}
		samples = n
	}

	client := anthropic.NewClient()
	var cards []eval.Scorecard
	// Fixtures that produced nothing at all. They leave the population, which
	// moves Sum's denominator, and a rung compared against another over a
	// different denominator is not a comparison. Named in the result line
	// rather than left to the errors above, so the number is read with them.
	var failed []string
	for _, f := range fixtures {
		if f.Session == nil || f.Session.Change == nil || len(f.Session.Change.Files) == 0 {
			continue
		}

		system, user := ladderPrompt(t, f, rung)
		var revs []findings.Review
		for si := range samples {
			out, err := ladderCall(client, model, system, user)
			if err != nil {
				t.Errorf("%s sample %d: %v", f.Annotation.Name, si, err)
				continue
			}
			rev, err := review.ParseReply(out)
			if err != nil {
				t.Errorf("%s sample %d: %v (got %s)", f.Annotation.Name, si, err, clip(out, 300))
				continue
			}
			revs = append(revs, *rev)
		}
		if len(revs) == 0 {
			failed = append(failed, f.Annotation.Name)
			continue
		}
		card := eval.ScoreSamples(f, revs)
		cards = append(cards, card)
		t.Logf("%s: comments=%d caught=%v caughtIn=%v missed=%d extra=%d",
			f.Annotation.Name, card.Comments, card.Caught, card.CaughtIn,
			len(card.Missed), card.Extra)
	}
	if len(cards) == 0 {
		t.Fatal("no fixture produced a review, so there is nothing to compare; " +
			"the errors above are the result")
	}
	tot := eval.Sum(cards)
	t.Logf("RUNG %s x%d: caught %d/%d (union), %d unlabelled, %d false positives, clean %d/%d",
		rung, samples, tot.Caught, tot.Expected, tot.Extra, tot.FalsePositives(),
		tot.CleanFixtures, len(cards))
	if len(failed) > 0 {
		t.Errorf("%d fixture(s) produced nothing and are not in the denominator: %v; "+
			"this rung's %d is not comparable with a rung that scored them",
			len(failed), failed, tot.Expected)
	}
}

// ladderPrompt builds the rung's two halves out of what actually ships:
// review.Assemble is the same call redline review makes, so rungs 1 and 2
// send the same bytes as the product rather than a reconstruction of them.
func ladderPrompt(t *testing.T, f eval.Fixture, rung string) (system, user string) {
	t.Helper()
	if rung == "0" {
		var b strings.Builder
		for _, file := range f.Session.Change.Files {
			if strings.TrimSpace(file.Diff) == "" {
				continue
			}
			fmt.Fprintf(&b, "--- %s (%s, +%d -%d)\n%s\n\n",
				file.Path, file.Status, file.Added, file.Removed, file.Diff)
		}
		return minimalSystem, b.String()
	}
	res, err := review.Assemble(review.Input{
		Report: &f.Session.Report, Change: f.Session.Change,
		Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
	}, review.Options{Model: os.Getenv("REDLINE_EVAL_MODEL")})
	if err != nil {
		t.Fatalf("%s: assemble: %v", f.Annotation.Name, err)
	}
	if rung == "2" {
		// The shipped system prompt says nothing about JSON: that contract
		// lives in the tool schema, so stripping the tool leaves a prompt
		// that answers in markdown. Appending the same contract the minimal
		// rung uses is what makes rung 2 the shipped prompt rather than the
		// shipped prompt minus its output format.
		return res.System + "\n\n" + jsonContract, res.Prompt
	}
	return minimalSystem, res.Prompt
}

func ladderCall(client anthropic.Client, model, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	// Non-streaming is rejected outright above a ten-minute ceiling, and at a
	// smaller cap it came back with no content blocks at all rather than an
	// error, which reads as a silent model and is not one.
	// Thinking is on by default and will spend the whole output budget before
	// a single text token: three fixtures came back stop=max_tokens with
	// nothing but thinking blocks, which reads as a silent model and is the
	// harness's fault. The rungs are about what the material buys, not about
	// the reasoning dial, so it is off here.
	//
	// With it off the model reasons in prose instead, and on the larger
	// fixtures that prose ate the budget before the object: all three samples
	// of gorefactor-nil-rules at rung 0 opened "Let me analyze the diff for
	// defects" and one never reached a brace at all, which dropped the fixture
	// out of the denominator and made the rung incomparable. Prefilling the
	// assistant turn with the opening brace would foreclose the preamble, and
	// the endpoint these rungs run against refuses it: assistant prefill comes
	// back 400, the conversation has to end with a user message. So the budget
	// check below is what names the cause instead of leaving it as a parse
	// failure.
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: ladderMaxTokens,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	var msg anthropic.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return "", err
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	var b strings.Builder
	kinds := make([]string, 0, len(msg.Content))
	for _, block := range msg.Content {
		kinds = append(kinds, string(block.Type))
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", fmt.Errorf("empty reply: stop=%s blocks=%v in=%d out=%d",
			msg.StopReason, kinds, msg.Usage.InputTokens, msg.Usage.OutputTokens)
	}
	// A reply cut off at the budget cannot parse, and reported as a parse
	// failure it reads as a model that cannot follow the contract. Naming the
	// cause here is what tells a raised ceiling apart from a broken prompt.
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		return "", fmt.Errorf("reply hit the %d token budget and is truncated: in=%d out=%d",
			ladderMaxTokens, msg.Usage.InputTokens, msg.Usage.OutputTokens)
	}
	return b.String(), nil
}

// clip shows both ends of a reply. The head alone said only that the model
// opened with prose, which was true of the replies that parsed too; whether
// the object ever arrived is visible at the tail.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	half := n / 2
	return s[:half] + "\n...\n" + s[len(s)-half:]
}
