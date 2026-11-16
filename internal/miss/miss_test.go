package miss

import (
	"path/filepath"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
	"github.com/chrisophus/redline/internal/target"
)

const head = "aaaaaaa1111111111111111111111111111111aa"

// session saves a reviewed head: one file with lines 10 and 11 added, one
// finding on line 30 of another file.
func session(t *testing.T, trace *postmortem.Trace) string {
	t.Helper()
	dir := t.TempDir()
	tgt := &target.Target{Kind: target.KindPR, Head: head, PR: &target.PullRequest{Number: 46}}
	res := &run.Result{
		Report: findings.Report{Findings: []findings.Finding{
			{File: "web/other.html", Line: 30, ID: "f1"},
			{File: "web/ranged.html", StartLine: 5, Line: 9, ID: "f2"},
		}},
		Target: tgt,
		Change: &change.Set{Target: tgt, Files: []change.File{
			{Path: "web/operator.html", Status: "modified", AddedLines: []int{10, 11}},
			{Path: "web/other.html", Status: "modified", AddedLines: []int{30}},
			{Path: "web/ranged.html", Status: "modified", AddedLines: []int{5, 6, 7, 8, 9}},
		}},
	}
	if err := run.SaveSession(dir, res); err != nil {
		t.Fatal(err)
	}
	if trace != nil {
		if err := postmortem.Write(dir, trace); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func flag(b *bool) string {
	switch {
	case b == nil:
		return "null"
	case *b:
		return "true"
	}
	return "false"
}

func examine(t *testing.T, dir, file string, line int) Entry {
	t.Helper()
	e := Entry{File: file, Line: line}
	if err := Examine(&e, dir); err != nil {
		t.Fatal(err)
	}
	return e
}

// The dropped `)` case: a line the change added, that nothing looked at.
func TestADefectOnAnAddedLineNobodyTouched(t *testing.T) {
	e := examine(t, session(t, nil), "web/operator.html", 11)
	got := flag(e.FileInDiff) + " " + flag(e.LineInDiff) + " " + flag(e.FindingTouched) + " " + flag(e.LookupTouched)
	if got != "true true false false" {
		t.Errorf("file/line in diff, finding, lookup = %s, want true true false false", got)
	}
}

func TestALineOutsideTheDiffIsSaidSo(t *testing.T) {
	e := examine(t, session(t, nil), "web/operator.html", 99)
	if !*e.FileInDiff || *e.LineInDiff {
		t.Errorf("file in diff %s, line in diff %s; want the file in and the line out", flag(e.FileInDiff), flag(e.LineInDiff))
	}
	e = examine(t, session(t, nil), "web/never-changed.html", 1)
	if *e.FileInDiff || *e.LineInDiff {
		t.Errorf("a file the change did not touch was reported in the diff")
	}
}

func TestAFindingTouchesItsLineOrRange(t *testing.T) {
	dir := session(t, nil)
	if e := examine(t, dir, "web/other.html", 30); !*e.FindingTouched {
		t.Error("a finding anchored on the line did not count")
	}
	if e := examine(t, dir, "web/other.html", 31); *e.FindingTouched {
		t.Error("a finding on line 30 counted for line 31")
	}
	if e := examine(t, dir, "web/ranged.html", 7); !*e.FindingTouched {
		t.Error("a ranged finding did not count for a line inside the range")
	}
}

func TestALookupTouchesALineItRead(t *testing.T) {
	trace := &postmortem.Trace{
		Findings: []postmortem.Finding{{ID: "c1", File: "web/operator.html", Line: 10}},
		Lookup: postmortem.Lookup{Filed: []postmortem.Filed{
			{File: "web/operator.html", StartLine: 20, EndLine: 40},
		}},
	}
	dir := session(t, trace)
	e := examine(t, dir, "web/operator.html", 30)
	if !*e.LookupTouched || *e.FindingTouched {
		t.Errorf("line 30: lookup %s finding %s; want a lookup and no finding", flag(e.LookupTouched), flag(e.FindingTouched))
	}
	// A proposed finding the ruling later dropped is still a finding that touched it.
	if e := examine(t, dir, "web/operator.html", 10); !*e.FindingTouched {
		t.Error("a proposed finding in the trace did not count")
	}
	if e := examine(t, dir, "web/operator.html", 11); *e.FindingTouched || *e.LookupTouched {
		t.Error("line 11 was touched by something that was not near it")
	}
}

func TestALookupThatOnlyNamedTheFileDoesNotCount(t *testing.T) {
	trace := &postmortem.Trace{Lookups: []review.Lookup{{Tool: "grep", Input: `{"pattern":"x","path":"web/operator.html"}`}}}
	e := examine(t, session(t, trace), "web/operator.html", 11)
	if *e.LookupTouched {
		t.Error("naming the file counted as reading the line")
	}
	if len(e.Touched) != 1 {
		t.Errorf("touched = %v, want the naming lookup listed", e.Touched)
	}
}

func TestExamineWithoutASessionIsAnError(t *testing.T) {
	e := Entry{File: "a", Line: 1}
	if err := Examine(&e, t.TempDir()); err == nil {
		t.Error("examining a directory with no session returned no error")
	}
}

func TestTheLedgerAppendsAndReadsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if got, err := Read(dir); err != nil || len(got) != 0 {
		t.Fatalf("a missing ledger = %v, %v; want empty", got, err)
	}
	for _, n := range []int{1, 2} {
		if err := Append(dir, Entry{PR: 46, File: "f", Line: n, Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(dir)
	if err != nil || len(got) != 2 || got[0].Line != 1 || got[1].Line != 2 {
		t.Fatalf("read back %+v, %v; want both entries in order", got, err)
	}
	// A miss recorded with no session keeps its checks null.
	if got[0].LineInDiff != nil {
		t.Error("an entry with no session came back with a line check")
	}
}

func TestHeldNamesTheHeadsAMissDependsOn(t *testing.T) {
	held := Held([]Entry{
		{PR: 46, Head: head, Session: "x"},
		{PR: 12, Head: "dead", Session: ""},
	})
	if !held[HeadKey(46, head)] || len(held) != 1 {
		t.Errorf("held = %v, want only the entry that has a session", held)
	}
}
