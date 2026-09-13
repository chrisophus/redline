package review

import "testing"

// The brief path parses a reply that nothing constrained. No tool grammar
// bounds it, and thinking is disabled on this path, so the model's reasoning
// arrives in the same channel the object does. These are the three shapes one
// fixture produced at three samples, plus the splice that used to break on the
// first of them.
func TestTheObjectIsFoundInWhateverTheReplyWrapsItIn(t *testing.T) {
	const obj = `{"overview":"o","files":[],"comments":[],"verdicts":[]}`
	cases := []struct {
		name  string
		reply string
	}{
		{"bare", obj},
		{"fenced", "```json\n" + obj + "\n```"},
		{"report tags", "<report>\n" + obj + "\n</report>"},
		{"prose heading first", "**Analysis notes**\n\nSome thinking.\n\n" + obj},
		{"trailing prose", obj + "\n\nThat is the review."},
		// The one that failed in the sweep. A brace quoted inside the prose
		// used to become the opening of the spliced object.
		{"reasoning quoting code", "<think>\n1. `p.Repo.File(\"\", f)` reads the worktree.\n" +
			"2. `if len(res.Findings) == 0 { appendConfirmation() }` is the tell.\n</think>\n" + obj},
		{"code after the object", obj + "\nNote: `func f() { return }` is unrelated."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := jsonObjectOf(c.reply)
			if got != obj {
				t.Errorf("got %q, want the review object", got)
			}
		})
	}
}

// The prose around the object is not JSON and does not pair its quotes. A
// single apostrophe or an opening quote the model never closes desyncs a
// string-aware scan, and from there every brace reads as text. This is the
// shape that survived the first fix and still failed on the wire.
func TestAnOddQuoteInTheProseDoesNotHideTheObject(t *testing.T) {
	const obj = `{"overview":"o","files":[],"comments":[],"verdicts":[]}`
	// One quote, never closed, which is what makes this a test: with an even
	// number the string-aware scan returns to its senses on its own and finds
	// the object without help.
	reply := "<think>\nThe pane's Diff method takes an \"after observation it never uses.\n" +
		"See `if n == 0 { return }` above.\nNow I will write the review.\n</think>\n" + obj
	// The pass this is about, on its own, so the case cannot quietly stop
	// exercising the thing it was written for. If this ever finds the object,
	// the reply above has lost its odd quote and the test below is vacuous.
	if got := scanForObject(reply, true); got != "" {
		t.Fatalf("the string-aware pass found %q, so this reply no longer desyncs it", got)
	}
	if got := jsonObjectOf(reply); got != obj {
		t.Errorf("got %q, want the review object", got)
	}
}

// A reply with no object at all is handed back whole, so the parser error that
// follows quotes what actually arrived.
func TestAReplyWithNoObjectIsReturnedUnchanged(t *testing.T) {
	const reply = "I could not review this change."
	if got := jsonObjectOf(reply); got != reply {
		t.Errorf("got %q, want the reply unchanged", got)
	}
}

// The review object encloses every object in the reply that belongs to it, so
// a snippet quoted beside it must not win on being valid JSON.
func TestTheEnclosingObjectWinsOverASnippet(t *testing.T) {
	const obj = `{"overview":"o","files":[],"comments":[{"file":"a.go","line":1,"body":"b"}],"verdicts":[]}`
	reply := "Here is an example of the shape: {\"file\":\"x.go\"}\n\n" + obj
	if got := jsonObjectOf(reply); got != obj {
		t.Errorf("got %q, want the review object", got)
	}
}
