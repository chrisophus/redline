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
// Measured 2026-09-13 on claude-sonnet-5, eleven fixtures, one sample: rung 0
// caught 12/38, rung 1 caught 11/38, and the shipped pipeline caught 6/38.
// The layers the product adds past the diff halve recall, and the packet's
// measurable contribution on this set is noise rather than catches: 25
// unlabelled comments at rung 0 against 16 at rung 1.
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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	only := os.Getenv("REDLINE_EVAL_FIXTURE")

	client := anthropic.NewClient()
	var cards []eval.Scorecard
	for _, f := range fixtures {
		if only != "" && f.Annotation.Name != only {
			continue
		}
		if f.Session == nil || f.Session.Change == nil || len(f.Session.Change.Files) == 0 {
			continue
		}

		system, user := ladderPrompt(t, f, rung)
		out, err := ladderCall(client, model, system, user)
		if err != nil {
			t.Errorf("%s: %v", f.Annotation.Name, err)
			continue
		}
		rev, err := ladderParse(t, out)
		if err != nil {
			t.Errorf("%s: %v (got %s)", f.Annotation.Name, err, clip(out, 300))
			continue
		}
		card := eval.Score(f, *rev)
		cards = append(cards, card)
		t.Logf("%s: comments=%d caught=%v missed=%d extra=%d",
			f.Annotation.Name, card.Comments, card.Caught, len(card.Missed), card.Extra)
	}
	tot := eval.Sum(cards)
	t.Logf("RUNG %s: caught %d/%d, %d unlabelled, %d false positives, clean %d/%d",
		rung, tot.Caught, tot.Expected, tot.Extra, tot.FalsePositives(), tot.CleanFixtures, len(cards))
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
	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 32000,
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
	return b.String(), nil
}

// ladderParse takes the model's free text down to the JSON object and runs it
// through the same loader an agent's review.json goes through.
func ladderParse(t *testing.T, out string) (*findings.Review, error) {
	t.Helper()
	s := strings.TrimSpace(out)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.IndexByte(s, '\n'); j >= 0 {
			s = s[j+1:]
		}
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return nil, fmt.Errorf("no JSON object in the reply")
	}
	s = s[i : j+1]
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("reply is not valid JSON")
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return nil, err
	}
	rev, err := findings.LoadReview(path)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, fmt.Errorf("loader returned no review")
	}
	return rev, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
