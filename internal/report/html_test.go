package report

import (
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/target"
)

func TestHighlightDiffForUsesSourceLineNumbers(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n" +
		"--- a/foo.go\n" +
		"+++ b/foo.go\n" +
		"@@ -10,3 +10,4 @@ func x() {\n" +
		" context\n" +
		"-old\n" +
		"+new\n" +
		" more\n"
	got := highlightDiffFor("foo.go", diff)
	if !strings.Contains(got, `data-file="foo.go" data-line="11" data-side="new">+new</span>`) {
		t.Fatalf("added line should be new-file line 11, got:\n%s", got)
	}
	if !strings.Contains(got, `data-file="foo.go" data-line="11" data-side="old">-old</span>`) {
		t.Fatalf("deleted line should be old-file line 11, got:\n%s", got)
	}
	if !strings.Contains(got, `data-line="10" data-side="new"> context</span>`) {
		t.Fatalf("context line should be new-file line 10, got:\n%s", got)
	}
	if strings.Contains(got, `data-line="7"`) || strings.Contains(got, `data-line="8"`) {
		t.Fatalf("must not use dump-row indexes:\n%s", got)
	}
}

func TestReviewIdentityDiffersForWorktreeChanges(t *testing.T) {
	base := "aaaaaaaaaaaaaaaa"
	a := reviewIdentity(base, "worktree", &packet.Packet{
		Files: []packet.FileChange{{Path: "a.go", Diff: "+one"}},
	})
	b := reviewIdentity(base, "worktree", &packet.Packet{
		Files: []packet.FileChange{{Path: "a.go", Diff: "+two"}},
	})
	if a == b {
		t.Fatal("different working-tree diffs must not share a comment key")
	}
	c := reviewIdentity(base, "bbbbbbbbbbbbbbbb", nil)
	d := reviewIdentity(base, "bbbbbbbbbbbbbbbb", nil)
	if c != d {
		t.Fatal("same base+head SHA must share a comment key")
	}
}

func TestHTMLIdentityAttribute(t *testing.T) {
	html, err := HTML(HTMLInput{
		Report: &findings.Report{BaseSHA: "abcdef0123456789", Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 1}},
		Packet: &packet.Packet{
			Target: &target.Target{Kind: target.KindBranch, Head: "ffffffffffffffff"},
			Files:  []packet.FileChange{{Path: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n", Areas: []string{"code"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `data-review="abcdef01:ffffffff"`) {
		t.Fatalf("expected identity in body, got a prefix of:\n%s", html[:400])
	}
}

func TestMarkdownSection3DoesNotClaimCompleteCoverage(t *testing.T) {
	rep := findings.Report{
		Coverage: findings.Coverage{
			ChangedFiles:  2,
			ExaminedFiles: 1,
			Unexamined:    []string{"README.md"},
		},
	}
	md := Markdown(&rep, nil, nil)
	if strings.Contains(md, "Nothing. Every check that applies") {
		t.Fatal("section 3 must not claim every check ran when files are unexamined")
	}
	if !strings.Contains(md, "were not in any pane's scope") {
		t.Fatalf("expected an unexamined-files sentence, got:\n%s", md)
	}
}
