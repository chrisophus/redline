package conversation

import (
	"testing"
	"time"
)

func TestSendReplyAndResend(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if r, err := Load(dir); err != nil || len(r.Items) != 0 {
		t.Fatalf("empty dir: %v %v", r, err)
	}
	if err := AddSent(dir, []Item{{ID: "a", File: "x.go", Line: 3, Text: "why"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := SetReply(dir, "a", "  because  ", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Sending the same comment again, edited, keeps the reply and the one entry.
	if err := AddSent(dir, []Item{{ID: "a", File: "x.go", Line: 3, Text: "why, really"}, {ID: "b", Text: "and this"}}, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 || r.Items[0].Text != "why, really" || r.Items[0].Reply != "because" || r.Items[1].ID != "b" {
		t.Errorf("record = %+v", r.Items)
	}
}

func TestSetReplyRefusesUnknownIDAndEmptyText(t *testing.T) {
	dir := t.TempDir()
	if err := AddSent(dir, []Item{{ID: "a"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SetReply(dir, "nope", "hi", time.Now()); err == nil {
		t.Error("reply to an unknown id was accepted")
	}
	if err := SetReply(dir, "a", "  ", time.Now()); err == nil {
		t.Error("empty reply was accepted")
	}
	if err := AddSent(dir, []Item{{Text: "no id"}}, time.Now()); err == nil {
		t.Error("an item with no id was accepted")
	}
}
