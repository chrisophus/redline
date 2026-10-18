package post

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/conversation"
)

func sampleConversation() []conversation.Item {
	return []conversation.Item{
		{ID: "c1", Action: "comment", File: "migrations/002.sql", Line: 1, Side: "new", Text: "why a new table?", Reply: "Because the old one is shared."},
		{ID: "c2", Action: "explain", File: "README.md", Line: 900, Side: "new"},
		{ID: "c3", Action: "comment", File: "migrations/002.sql", Line: 4, Side: "old", Text: "was this removed on purpose?"},
	}
}

// An exchange on a changed line becomes a line comment carrying both sides; one
// off the diff, or on a removed line, goes in the body instead, since a single
// comment GitHub cannot anchor rejects the whole review.
func TestWithConversationAnchorsWhatItCanAndBodiesTheRest(t *testing.T) {
	commentable := map[string]map[int]bool{"migrations/002.sql": {1: true, 4: true}}
	p := Build(sampleReport(), prTarget(), "", commentable).WithConversation(sampleConversation(), commentable)

	var line *Comment
	for i := range p.Comments {
		if p.Comments[i].Fingerprint == "conversation:c1:replied" {
			line = &p.Comments[i]
		}
	}
	if line == nil {
		t.Fatalf("no line comment for c1 in %+v", p.Comments)
	}
	for _, want := range []string{"**Reviewer**", "> why a new table?", "**Agent reply**", "Because the old one is shared.", fpMarkerPrefix} {
		if !strings.Contains(line.Body, want) {
			t.Errorf("line comment lacks %q:\n%s", want, line.Body)
		}
	}
	if line.Path != "migrations/002.sql" || line.Line != 1 || line.Side != "RIGHT" {
		t.Errorf("line comment anchored at %s:%d %s", line.Path, line.Line, line.Side)
	}
	for _, want := range []string{"### Review conversation", "`README.md:900`", "Asked the agent to explain this.", "was this removed on purpose?"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	if p.NothingNew() {
		t.Error("NothingNew with exchanges to post")
	}
}

// An exchange already on the pull request is not posted again, but one that
// was posted before the agent answered is, once the answer is there.
func TestConversationIsIdempotentUntilAReplyArrives(t *testing.T) {
	items := []conversation.Item{{ID: "c9", File: "elsewhere.go", Line: 3, Text: "hm"}}
	first := Build(sampleReport(), prTarget(), "", map[string]map[int]bool{}).WithConversation(items, map[string]map[int]bool{})
	posted := Fingerprints([]string{first.Body})

	again := first.Unposted(posted)
	if strings.Contains(again.Body, "### Review conversation") {
		t.Error("an exchange already posted was posted again")
	}

	items[0].Reply = "done"
	answered := Build(sampleReport(), prTarget(), "", map[string]map[int]bool{}).WithConversation(items, map[string]map[int]bool{}).Unposted(posted)
	if !strings.Contains(answered.Body, "**Agent reply**") {
		t.Error("the answered exchange was held back as already posted")
	}
}
