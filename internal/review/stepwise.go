package review

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// The stepwise pipeline: one conversation in two turns over the same model.
//
// The synopsis split sends two independent calls that both carry the whole
// packet, so the judging call never sees the walkthrough the describing call
// wrote and the describing call reads context it was told to ignore. Stepwise
// discloses the packet in order instead. Turn 1 is the change and its diff and
// the instruction to describe them. Turn 2 appends turn 1's answer unchanged,
// answers its tool call, and only then hands over what turn 1 did not have -
// the established findings, what the pull request already heard, coverage,
// the producers that did not run, and the context envelopes - with the
// instruction to judge.
//
// Whether understanding the change from the diff first raises recall or cuts
// noise is the question the arm exists to answer. What the code guarantees is
// the cost shape: the conversation is append-only, so turn 2 resends turn 1's
// bytes behind a breakpoint and reads them back from the cache.
//
// Anthropic wire only. Turn 2 has to resend turn 1's assistant message as the
// model produced it, thinking blocks and their signatures included, and the
// OpenAI wire has no equivalent this package builds.

// stepwiseAck is what turn 2 answers turn 1's tool call with. The endpoint
// refuses a conversation that goes on past an unanswered tool_use, and the
// walkthrough is already recorded on this side, so the result says only that.
//
//nolint:gosec // G101 reads the "pw" in "stepwise" as a password.
const stepwiseAck = "Recorded. The rest of the packet follows."

// stepwiseRefusal is every combination the stepwise conversation cannot run
// under, checked before anything is priced or sent.
func stepwiseRefusal(opts Options) error {
	switch {
	case opts.API == APIOpenAI:
		// Refused outright. An option that quietly did something else on the
		// OpenAI wire is how Brief came apart between the two wires, and a
		// stepwise row scored off a one-shot call would compare two arms that
		// were the same arm.
		return fmt.Errorf("--stepwise is one conversation that resends its first turn as the model wrote it, "+
			"and that conversation is only built on the Anthropic wire; use --api %s, or drop --stepwise with --api %s",
			APIAnthropic, APIOpenAI)
	case opts.Samples > 1:
		return fmt.Errorf("--samples unions independent reviews, and a stepwise run is two turns of one conversation; " +
			"sampling it doubles every sample's calls")
	case opts.Cohorts > 1:
		// Both decide what the describing call is: stepwise describes from the
		// diff alone and draws no partition, and the split needs one drawn.
		return fmt.Errorf("--stepwise describes the change from its diff before the rest arrives and draws no partition, " +
			"and --cohorts above 1 is judged over one; pass one or the other")
	case opts.Brief:
		return fmt.Errorf("--brief is a single pass under the review contract, and --stepwise writes the walkthrough " +
			"and the findings in two turns under contracts the short prompt does not describe; pass one or the other")
	case opts.Mode == ModeExplore:
		return fmt.Errorf("--stepwise and --mode explore are both multi-turn conversations over the packet; pick one")
	}
	return nil
}

// stepwiseOpening is turn 1's user text: what the change is, its diff, and
// the describing instruction with the roster of files to describe. Nothing
// else from the packet goes in, which is the whole point of the shape.
func (in Input) stepwiseOpening() string {
	return in.changeSection() + in.diffSection() + stepwiseDescribeTail(in)
}

// stepwiseRest is turn 2's material: everything the full prompt carries that
// turn 1 did not have, in the order build puts it. The context block is the
// one Assemble already fitted to the ceiling, so the budget that priced the
// run is the budget that is sent.
func (in Input) stepwiseRest(budget envelope.Budgeted) string {
	s := stepwiseLead + in.priorsSection() + in.heardSection() + in.coverageSection() + in.absentSection()
	if ctx := budget.Render(); ctx != "" {
		s += in.contextHeader(keptRoles(budget)) + ctx + "\n"
	}
	return s
}

// stepwiseDescribeRequest is turn 1. The whole user turn is one block, so the
// breakpoint anthropicParams places on the prompt block sits at the end of
// turn 1's content and turn 2 reads all of it back.
func (r *Result) stepwiseDescribeRequest(opts Options, in Input) *Result {
	out := r.clone()
	out.Stage = StageSynopsis
	// The describing instruction is part of the block, not a block after it.
	// The breakpoint sits at the end of this one and turn 2 resends exactly
	// what turn 1 sent to read it back; a second block would fall outside that
	// and be paid for twice.
	out.Prompt = in.stepwiseOpening() + describingTail
	out.Tail = ""
	out.InputEstimate = envelope.EstimateTokens(r.System) + envelope.EstimateTokens(out.Prompt)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedSynopsisTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
}

// stepwiseJudgeRequest is turn 2, without the conversation it continues: that
// is attached by runStepwise once turn 1 has answered. written is what turn 1
// wrote, which turn 2 resends as input: the measured count on a real run, and
// the cap when the tripwire prices a turn 1 that has not happened.
func (r *Result) stepwiseJudgeRequest(opts Options, in Input, one *Result, written int64) *Result {
	out := r.clone()
	out.Stage = StageFindings
	out.Prompt = in.stepwiseRest(r.Budget)
	out.Tail = findingsPrompt
	out.InputEstimate = one.InputEstimate + int(written) + envelope.EstimateTokens(stepwiseAck) +
		envelope.EstimateTokens(out.Prompt) + envelope.EstimateTokens(out.Tail)
	out.CostUSD, out.CostKnown = EstimateCost(opts.Model, out.InputEstimate, ExpectedOutputTokens)
	out.CostCeilingUSD, _ = CeilingCost(opts.Model, out.InputEstimate, opts.MaxTokens)
	return out
}

// stepwiseCeilingCost is what the conversation adds to the tripwire's worst
// case, and zero when the run is some other shape.
//
// Turn 1 is priced at its own input and the whole cap. Turn 2 is priced at the
// full conversation it resends, with turn 1's input at the cache-read rate
// when the breakpoint is on and at full rate when it is off, plus turn 1's
// answer at the cap, since a turn 1 that answered wrote less than that, plus
// its own cap.
//
// The tripwire already counts the one-shot call, and a stepwise run makes
// turn 1 and then exactly one of turn 2 and the one-shot fallback. So what is
// added is turn 1 and whatever turn 2 costs above the call already counted.
// Summing all three would price a run nobody can make, and at a 64,000-token
// cap that is three full output allowances, which refuses every real fixture
// at the $2 default for a request that never goes out.
func stepwiseCeilingCost(opts Options, in Input, res *Result) float64 {
	if !opts.Stepwise || res == nil {
		return 0
	}
	one := res.stepwiseDescribeRequest(opts, in)
	first, ok := CeilingCost(opts.Model, one.InputEstimate, opts.MaxTokens)
	if !ok {
		return 0
	}
	two := res.stepwiseJudgeRequest(opts, in, one, opts.MaxTokens)
	resent := Usage{
		InputTokens:  int64(two.InputEstimate - one.InputEstimate),
		OutputTokens: opts.MaxTokens,
	}
	if opts.cacheOn() {
		resent.CacheReadTokens = int64(one.InputEstimate)
	} else {
		resent.InputTokens += int64(one.InputEstimate)
	}
	second, _ := resent.Cost(opts.Model)
	return first + max(0, second-res.CostCeilingUSD)
}

// stepwiseTurn labels one turn's debug lines and captured files. Turn 1 and
// the synopsis path's describing call force the same tool and would otherwise
// write files of the same name, and a reader comparing two captures is looking
// for exactly which conversation a request belonged to.
func stepwiseTurn(opts Options, n int) Options {
	o := opts
	label := fmt.Sprintf("turn%d", n)
	if opts.Capture != nil {
		o.Capture = func(name string, data []byte) { opts.Capture("stepwise-"+label+"-"+name, data) }
	}
	if opts.Debug != nil {
		o.Debug = func(line string) { opts.Debug("stepwise " + label + ": " + line) }
	}
	return o
}

// runStepwise is the conversation: describe from the diff, then judge with
// the rest of the packet.
//
// Turn 1 never fails the review. A turn that errors, refuses, truncates or
// writes no overview leaves the run on the one-shot contract, the way describe
// and runStaged degrade, and says why. The fallback call sends the same system
// block and the same catalogue as turn 1, so it still reads those back. Turn 2
// failing fails the run, because it is the judging call and there is nothing
// left to fall back to that has not already been paid for.
func runStepwise(ctx context.Context, in Input, opts Options, res *Result) (*Result, error) {
	if opts.Progress != nil {
		opts.Progress("describing the change from its diff before showing the rest of the packet")
	}
	one := res.stepwiseDescribeRequest(opts, in)
	// Built before the call and from the same function the call builds its
	// request with, so the user turn resent in turn 2 is the value turn 1
	// sent, breakpoint included.
	opening := anthropicParams(opts, one).Messages
	got, err := runOnce(ctx, in, stepwiseTurn(opts, 1), one)
	walkthrough, usage, written, failed := describedBy(got, err)
	if failed != "" {
		if opts.Progress != nil {
			opts.Progress("the describing turn did not produce a walkthrough (" + failed +
				"); this review is one call and writes its own")
		}
		out, rerr := runOnce(ctx, in, opts, res)
		// Turn 1 was billed whether or not it answered, so it is folded in
		// here for the reason runStaged folds its stage one in.
		applySynopsis(out, opts.Model, findings.Review{}, usage, written, failed)
		if out != nil {
			out.Pipeline = PipelineOneShot
			out.FellBack = failed
			out.Duration += got.Duration
		}
		return out, rerr
	}
	if opts.Progress != nil {
		opts.Progress(fmt.Sprintf("described %d file(s) in %s; judging with the rest of the packet",
			len(walkthrough.Files), got.Duration.Round(time.Second)))
	}

	two := res.stepwiseJudgeRequest(opts, in, one, written)
	// The assistant turn as the SDK converted it off the response, every block
	// in the order the model wrote it. Rebuilt by hand it would drop whatever
	// this code did not think to copy, and a thinking block resent without its
	// signature is refused.
	two.history = append(append([]anthropic.MessageParam(nil), opening...), got.reply)
	two.answering = got.replyToolUses
	out, err := runOnce(ctx, in, stepwiseTurn(opts, 2), two)
	applySynopsis(out, opts.Model, walkthrough, usage, written, "")
	out.Duration += got.Duration
	out.Turns = 2
	// What the eval's recorder writes as the packet a sample reviewed. The
	// model was shown turn 1 and then turn 2, so the trace is both in that
	// order, and a reader of it sees which material the walkthrough was
	// written without.
	out.Prompt = one.Prompt + "\n\n" + two.Prompt
	out.history, out.answering = nil, nil
	return out, err
}
