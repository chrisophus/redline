package review

import (
	"context"
	"os"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/chrisophus/redline/internal/envelope"
)

// TestExploreCacheProbe answers one question the code cannot: does explore
// mode's cache get read back?
//
// The one-shot path's breakpoints were removed once the wire showed the
// response schema sitting in front of the cached prefix, so the review and
// the ruling each wrote an entry neither could read. Explore mode kept its
// breakpoints on the reasoning that its case is different - one growing
// conversation under one schema, where a later turn really should read an
// earlier one. That reasoning has never been checked against the wire, and
// the ledger cannot check it: it records a run's totals, so a turn that read
// nothing and a turn that read everything average into the same line.
//
// So this sends the same prefix twice, byte-identical, in the shape explore
// builds it, and reports all four meters for both turns. A second turn whose
// cache_read_input_tokens is zero means the breakpoints are a 1.25x surcharge
// on every turn and should come off the same way the one-shot's did.
//
// Paid, and opt-in for that reason:
//
//	REDLINE_CACHE_PROBE=claude-sonnet-5 go test ./internal/review -run CacheProbe -v
func TestExploreCacheProbe(t *testing.T) {
	model := os.Getenv("REDLINE_CACHE_PROBE")
	if model == "" {
		t.Skip("set REDLINE_CACHE_PROBE to a model id to run the paid cache probe (two calls)")
	}
	ctx := context.Background()
	client := anthropic.NewClient()

	// The real system block, padded to clear the minimum cacheable prefix.
	// A prefix under the model's floor silently will not cache, and a probe
	// that cannot tell "too short" from "invalidated" answers nothing.
	system := systemPrompt + exploreAddendum
	for envelope.EstimateTokens(system) < 4096 {
		system += "\n" + systemPrompt
	}

	// The same two breakpoints runExplore places, on the same blocks.
	opening := anthropic.NewBetaTextBlock("Probe. Reply with the single word ok.")
	opening.OfText.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 64,
		System: []anthropic.BetaTextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(opening)},
	}

	send := func(turn int) anthropic.BetaMessage {
		msg, err := client.Beta.Messages.New(ctx, params)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		t.Logf("turn %d: input=%d cache_write=%d cache_read=%d output=%d",
			turn, msg.Usage.InputTokens, msg.Usage.CacheCreationInputTokens,
			msg.Usage.CacheReadInputTokens, msg.Usage.OutputTokens)
		return *msg
	}

	// Either meter clears the precondition. A first turn that reads rather
	// than writes means an identical prefix from a run minutes ago is still
	// live, which is the same entry turn 2 is being asked about; only a turn
	// that neither wrote nor read leaves nothing to measure.
	first := send(1)
	cached := first.Usage.CacheCreationInputTokens + first.Usage.CacheReadInputTokens
	if cached == 0 {
		t.Fatalf("turn 1 neither wrote nor read a cache entry, so there is nothing for turn 2 to read; "+
			"the prefix may be under this model's minimum (%d tokens sent)", first.Usage.InputTokens)
	}

	// Turn two the way explore grows a conversation: the assistant's reply
	// appended, then a new user turn after it. Everything above the
	// breakpoints is byte-identical to turn one.
	params.Messages = append(params.Messages, first.ToParam(),
		anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock("Again. Reply with the single word ok.")))
	second := send(2)

	if second.Usage.CacheReadInputTokens == 0 {
		t.Errorf("turn 2 read nothing back from a %d-token entry turn 1 paid 1.25x to write. "+
			"Explore mode's breakpoints are a surcharge, not a saving: remove them the way "+
			"the one-shot path's were removed", cached)
		return
	}
	t.Logf("explore's breakpoints work: turn 2 read %d tokens at 0.1x that turn 1 wrote at 1.25x",
		second.Usage.CacheReadInputTokens)
}
