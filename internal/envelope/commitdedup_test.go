package envelope

import (
	"strings"
	"testing"
)

// git log -L is asked per line range, so a commit that touched many ranges
// ships its whole message once per range. Seen cannot catch it: that removes
// lines the diff already shows, and carriesHistory exempts these roles from it
// because their content is commit messages rather than source. The exemption
// is right and it left the one role with tenfold duplication undeduplicated.
func block(sha, subject, body, hunk string) string {
	return "commit " + sha + "\n" +
		"Author: Chris Cason <chris@example.com>\n" +
		"Date:   2026-09-18T18:41:26-06:00\n" +
		"\n" +
		"    " + subject + "\n" +
		"    \n" +
		"    " + body + "\n" +
		"\n" + hunk
}

func TestACommitMessageIsCarriedOnceForTheWholeChange(t *testing.T) {
	const sha = "1b1bab40c222b462e947f14b9c866a60daff39e2"
	essay := strings.Repeat("the reason these lines exist, at length. ", 40)
	first := Expansion{Role: RoleHistory, File: "a.go", StartLine: 14, EndLine: 45,
		Content: block(sha, "Check a diff question's claim", essay, "@@ -14,3 +14,4 @@\n+x\n")}
	second := Expansion{Role: RoleHistory, File: "a.go", StartLine: 67, EndLine: 94,
		Content: block(sha, "Check a diff question's claim", essay, "@@ -67,2 +67,3 @@\n+y\n")}
	ranked := []Expansion{first, second}
	before := len(ranked[0].Content) + len(ranked[1].Content)

	dedupeCommits(ranked)

	if !strings.Contains(ranked[0].Content, essay) {
		t.Error("the first expansion must keep the message in full")
	}
	if strings.Contains(ranked[1].Content, essay) {
		t.Error("the second must not repeat it")
	}
	if !strings.Contains(ranked[1].Content, "(rest of this message under a.go:14-45)") {
		t.Errorf("the second must say where to read it:\n%s", ranked[1].Content)
	}
	// What differs per range is kept: the header and git's hunk for it.
	for _, want := range []string{"commit " + sha, "@@ -67,2 +67,3 @@", "+y"} {
		if !strings.Contains(ranked[1].Content, want) {
			t.Errorf("the second must keep %q, which is range-specific:\n%s", want, ranked[1].Content)
		}
	}
	after := len(ranked[0].Content) + len(ranked[1].Content)
	if after >= before {
		t.Errorf("bytes went %d to %d, want the repeat removed", before, after)
	}
}

// A distinct commit keeps its own message, and a role that is not history is
// left alone: only these two carry commit messages, and rewriting source would
// be corruption.
func TestDedupeLeavesDistinctCommitsAndOtherRolesAlone(t *testing.T) {
	a := block("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "first", "reason one", "@@ -1 +1 @@\n+a\n")
	b := block("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "second", "reason two", "@@ -2 +2 @@\n+b\n")
	code := "func Insert() error {\n    return nil\n}\n"
	ranked := []Expansion{
		{Role: RoleHistory, File: "a.go", StartLine: 1, Content: a},
		{Role: RoleRemoval, File: "b.go", StartLine: 2, Content: b},
		{Role: RoleCaller, File: "c.go", StartLine: 3, Content: code},
	}
	dedupeCommits(ranked)

	if !strings.Contains(ranked[0].Content, "reason one") || !strings.Contains(ranked[1].Content, "reason two") {
		t.Error("two different commits each keep their own message")
	}
	if ranked[2].Content != code {
		t.Errorf("a caller expansion must pass through untouched:\n%q", ranked[2].Content)
	}
}

// The roles share one register of seen commits: a message carried by a history
// expansion is not carried again by a removal one.
func TestHistoryAndRemovalShareTheRegister(t *testing.T) {
	const sha = "cccccccccccccccccccccccccccccccccccccccc"
	essay := "why the guard was added in the first place"
	ranked := []Expansion{
		{Role: RoleHistory, File: "a.go", StartLine: 10, EndLine: 20,
			Content: block(sha, "add the guard", essay, "@@ -10 +10 @@\n+g\n")},
		{Role: RoleRemoval, File: "a.go", StartLine: 30, EndLine: 31,
			Content: block(sha, "add the guard", essay, "@@ -30 +30 @@\n-g\n")},
	}
	dedupeCommits(ranked)
	if strings.Contains(ranked[1].Content, essay) {
		t.Error("removal must not repeat a message history already carried")
	}
	if !strings.Contains(ranked[1].Content, "a.go:10-20") {
		t.Errorf("the reference must name where it was carried:\n%s", ranked[1].Content)
	}
}
