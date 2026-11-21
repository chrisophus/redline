package feedback

import (
	"testing"
)

func responseWith(reviews, threads string) []byte {
	return []byte(`{"data":{"repository":{"pullRequest":{"reviews":{"nodes":[` + reviews +
		`]},"reviewThreads":{"nodes":[` + threads + `]}}}}}`)
}

// A finding `post` folds into a review's summary has no thread, so it used to
// be invisible to the next review, which then said it again.
func TestASummaryFindingIsCarried(t *testing.T) {
	fp := "internal/studio/service.go\x00agent-comment\x00the check keys on the exact name"
	body := "### Review complete\\n\\n<details>\\n<summary>Low confidence (1)</summary>\\n\\n" +
		"- **Info · agent** — The check keys on the exact name. _(internal/studio/service.go:299 — not on a changed line in this PR)_ " +
		marker("abc123", fp) + "\\n</details>\\n"
	got, err := Parse(responseWith(`{"body": "`+body+`"}`, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want the one summary finding, got %d", len(got))
	}
	th := got[0]
	if !th.InBody {
		t.Error("a summary finding has to say it had no thread")
	}
	if th.File != "internal/studio/service.go" || th.Line != 299 {
		t.Errorf("location = %s:%d", th.File, th.Line)
	}
	if th.Said != "The check keys on the exact name." {
		t.Errorf("the label and the location are not what was said: %q", th.Said)
	}
	if th.Fingerprint != fp {
		t.Errorf("fingerprint = %q", th.Fingerprint)
	}
}

// A summary lists what the panes measured too. Those are measured again on
// every run and are not the reviewer's to repeat.
func TestAMeasuredSummaryFindingIsNotCarried(t *testing.T) {
	body := "- **Warning** — file-size: 569 lines " + marker("abc123", "internal/a.go\x00gorefactor/file-size\x00# lines")
	got, err := Parse(responseWith(`{"body": "`+body+`"}`, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a pane's finding was carried as something the reviewer said: %+v", got)
	}
}

// A later summary repeats a finding's marker when the finding is still there,
// and a finding with a thread is already carried with its replies.
func TestASummaryFindingIsCarriedOnce(t *testing.T) {
	fp := "internal/a.go\x00agent-comment\x00races"
	line := "- **Info · agent** — Races. " + marker("abc123", fp)
	thread := `{"isResolved": false, "isOutdated": false, "path": "internal/a.go", "line": 4,
      "comments": {"nodes": [{"body": "**Warning · agent** — Races.\n` + marker("abc123", fp) + `",
        "author": {"login": "redline"}, "reactionGroups": []}]}}`
	got, err := Parse(responseWith(`{"body": "`+line+`"}, {"body": "`+line+`"}`, thread))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].InBody {
		t.Fatalf("want the thread alone, got %+v", got)
	}
}
