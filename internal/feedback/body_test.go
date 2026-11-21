package feedback

import (
	"strings"
	"testing"
)

// responseWith is a reply in which every review was written by the login the
// token answers to, which is the one whose summaries are read.
func responseWith(reviews, threads string) []byte {
	reviews = strings.ReplaceAll(reviews, `{"body":`, `{"author": {"login": "redline"}, "body":`)
	return []byte(`{"data":{"viewer":{"login":"redline"},"repository":{"pullRequest":{"reviews":{"nodes":[` + reviews +
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

// A reviewer's comment can run over several lines, and the marker is on the
// last of them. Reading one line at a time kept only the tail of the comment.
func TestASummaryFindingOverSeveralLinesIsCarriedWhole(t *testing.T) {
	fp := "internal/a.go\x00agent-comment\x00the list is a hand copy"
	body := "- **Info · agent** — The list is a hand copy.\\n\\nNothing keeps it in sync:\\n- one\\n- two _(internal/a.go:15 — not on a changed line in this PR)_ " +
		marker("abc123", fp) + "\\n- **Info · agent** — Second finding. _(redline/review)_ " +
		marker("abc123", "internal/b.go\x00agent-comment\x00second finding")
	got, err := Parse(responseWith(`{"body": "`+body+`"}`, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want two findings, got %+v", got)
	}
	if want := "The list is a hand copy.\n\nNothing keeps it in sync:\n- one\n- two"; got[0].Said != want || got[0].Line != 15 {
		t.Errorf("first = %q at line %d", got[0].Said, got[0].Line)
	}
	// A finding with no line is followed by the name of what produced it,
	// which is not part of what was said.
	if got[1].Said != "Second finding." || got[1].Line != 0 {
		t.Errorf("second = %q at line %d", got[1].Said, got[1].Line)
	}
}

// A finding that connects two others is the reviewer's own too.
func TestACorrelationInASummaryIsCarried(t *testing.T) {
	body := "- **Warning · agent** — The column is NOT NULL and the field cannot be empty. " +
		marker("abc123", "internal/a.go\x00correlation\x00the column is not null")
	got, err := Parse(responseWith(`{"body": "`+body+`"}`, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].InBody {
		t.Fatalf("got %+v", got)
	}
}

// The marker is a hidden comment anyone could paste into a review of their
// own. Only a summary the posting login wrote is read.
func TestASummaryFromAnotherAuthorIsNotRead(t *testing.T) {
	line := "- **Info · agent** — Races. " + marker("abc123", "internal/a.go\x00agent-comment\x00races")
	raw := func(viewer, author string) []byte {
		return []byte(`{"data":{"viewer":{"login":"` + viewer + `"},"repository":{"pullRequest":{"reviews":{"nodes":[` +
			`{"author": {"login": "` + author + `"}, "body": "` + line + `"}]},"reviewThreads":{"nodes":[]}}}}}`)
	}
	if got, _ := Parse(raw("redline", "somebody-else")); len(got) != 0 {
		t.Errorf("a pasted marker was carried as something Redline said: %+v", got)
	}
	if got, _ := Parse(raw("", "redline")); len(got) != 0 {
		t.Errorf("with no login to compare against, nothing can be trusted: %+v", got)
	}
	t.Setenv(loginEnv, "redline-bot")
	if got, _ := Parse(raw("a-person", "redline-bot")); len(got) != 1 {
		t.Errorf("the login the environment names should be the one read: %+v", got)
	}
}

// A bot's login has a "[bot]" suffix in REST and may not in GraphQL, so a
// setup that posts as an app must still find its own summaries.
func TestABotLoginMatchesWithOrWithoutItsSuffix(t *testing.T) {
	for _, c := range []struct{ a, b string }{
		{"review-bot[bot]", "review-bot"},
		{"review-bot", "review-bot[bot]"},
		{"Review-Bot", "review-bot"},
	} {
		if !sameLogin(c.a, c.b) {
			t.Errorf("%q and %q are the same login", c.a, c.b)
		}
	}
	if sameLogin("review-bot", "other-bot[bot]") || sameLogin("", "") || sameLogin("[bot]", "") {
		t.Error("different or empty logins matched")
	}
}
