package miss

import (
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
)

func session() Session {
	return Session{
		Change: &change.Set{Files: []change.File{
			{Path: "web/operator.html", AddedLines: []int{10, 11, 12}},
			{Path: "other.go", AddedLines: []int{3}},
		}},
	}
}

func TestExamineSaysWhetherTheLineWasInTheDiff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		file     string
		line     int
		wantFile bool
		wantLine bool
	}{
		{"an added line", "web/operator.html", 11, true, true},
		{"a line the change left alone", "web/operator.html", 40, true, false},
		{"a file the change did not touch", "elsewhere.go", 11, false, false},
		{"a path spelled with ./", "./web/operator.html", 10, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Entry{File: tc.file, Line: tc.line}
			Examine(&e, session())
			if e.FileInDiff == nil || e.LineInDiff == nil {
				t.Fatal("an archived session left the diff answers unset")
			}
			if *e.FileInDiff != tc.wantFile || *e.LineInDiff != tc.wantLine {
				t.Errorf("fileInDiff=%v lineInDiff=%v, want %v and %v", *e.FileInDiff, *e.LineInDiff, tc.wantFile, tc.wantLine)
			}
		})
	}
}

func TestExamineFindsTheFindingsAndLookupsOnTheLine(t *testing.T) {
	s := session()
	s.Findings = []findings.Finding{
		{ID: "f1", File: "web/operator.html", Line: 12, StartLine: 10, Message: "range over it"},
		{ID: "f2", File: "web/operator.html", Line: 30, Message: "far away"},
		{ID: "f3", File: "other.go", Line: 11, Message: "other file"},
	}
	s.Trace = &postmortem.Trace{
		Findings: []postmortem.Finding{
			{ID: "c1", File: "web/operator.html", Line: 11, Body: "withdrawn later"},
			{ID: "c2", File: "web/operator.html", Line: 99},
		},
		Lookups: []review.Lookup{
			{Tool: review.CallRead, Input: `{"path":"web/operator.html","start_line":5,"end_line":20}`},
			{Tool: review.CallRead, Input: `{"path":"web/operator.html","start_line":50,"end_line":60}`},
			{Tool: review.CallGrep, Input: `{"pattern":"operator"}`},
		},
		Lookup: postmortem.Lookup{Filed: []postmortem.Filed{
			{Role: envelope.Role("callers"), File: "web/operator.html", StartLine: 11, EndLine: 11, Answers: "c1"},
			{Role: envelope.Role("callers"), File: "web/operator.html", Symbol: "x"},
		}},
	}
	e := Entry{File: "web/operator.html", Line: 11}
	Examine(&e, s)

	if got := len(e.Findings); got != 2 {
		t.Errorf("%d findings touched the line, want the ranged finding and the proposed one: %+v", got, e.Findings)
	}
	if got := len(e.Lookups); got != 2 {
		t.Errorf("%d lookups touched the line, want the read and the filed record: %+v", got, e.Lookups)
	}
}

func TestExamineWithNothingNearTheLineLeavesTheListsEmpty(t *testing.T) {
	e := Entry{File: "web/operator.html", Line: 11}
	Examine(&e, session())
	if len(e.Findings) != 0 || len(e.Lookups) != 0 {
		t.Errorf("findings %v lookups %v, want none", e.Findings, e.Lookups)
	}
}

func TestTheLedgerAppendsAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	if got, err := Read(dir); err != nil || got != nil {
		t.Fatalf("an empty directory read as %v, %v, want no misses", got, err)
	}
	yes := true
	for _, e := range []Entry{
		{PR: 46, Head: "abc", File: "a.html", Line: 3, Summary: "dropped a )", FixPR: 59, LineInDiff: &yes},
		{PR: 12, File: "reset.yml", Line: 1, Summary: "roles that never existed"},
	} {
		if err := Append(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].PR != 46 || got[0].LineInDiff == nil || !*got[0].LineInDiff || got[1].LineInDiff != nil {
		t.Errorf("read back %+v, want both entries with the unknown answer still unknown", got)
	}
}

func TestExamineWithNoChangeLeavesTheDiffAnswersUnset(t *testing.T) {
	e := Entry{File: "web/operator.html", Line: 11}
	Examine(&e, Session{})
	if e.FileInDiff != nil || e.LineInDiff != nil {
		t.Errorf("file %v line %v, want both unset", e.FileInDiff, e.LineInDiff)
	}
}
