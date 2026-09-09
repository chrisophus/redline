package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

// The report has rendered agent verdicts all along and the one command that
// calls a model could not produce one: the schema had no field for them, so
// only an agent writing review.json by hand could rule on a finding.
func TestTheReviewCanRuleOnADeterministicFinding(t *testing.T) {
	body := []byte(`{"overview":"o","files":[],"comments":[],
		"verdicts":[
		  {"finding":"fd98076e46a","ruling":"rule-noisy","rationale":"errcheck fires on every deferred Close here","fix":""},
		  {"finding":"fc03af77c90","ruling":"should-fix","rationale":"the skip hides a real gap","fix":"install git in CI"}
		]}`)
	rev, err := parseReview(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Verdicts) != 2 {
		t.Fatalf("verdicts = %v", rev.Verdicts)
	}
	v := rev.Verdicts["fd98076e46a"]
	if v.Ruling != "rule-noisy" || !strings.Contains(v.Rationale, "deferred Close") {
		t.Errorf("verdict = %+v", v)
	}
	// Redline states facts and the reader supplies the judgment, so a verdict
	// is always attributed to the model rather than to the instrument.
	if v.Source != findings.SourceLLM {
		t.Errorf("source = %q, want llm", v.Source)
	}
	if rev.Verdicts["fc03af77c90"].Fix != "install git in CI" {
		t.Error("the fix was dropped")
	}
}

// The findings and the overview are the review. A ruling that arrived without
// a finding to attach to is worth losing on its own, not worth losing them.
func TestAMalformedVerdictIsDroppedNotFatal(t *testing.T) {
	body := []byte(`{"overview":"o","files":[],"comments":[],
		"verdicts":[{"finding":"","ruling":"should-fix"},{"finding":"abc","ruling":""},
		            {"finding":"good","ruling":"justified","rationale":"deliberate"}]}`)
	rev, err := parseReview(body)
	if err != nil {
		t.Fatalf("a bad verdict took the whole review down: %v", err)
	}
	if len(rev.Verdicts) != 1 || rev.Verdicts["good"].Ruling != "justified" {
		t.Errorf("verdicts = %v, want only the well-formed one", rev.Verdicts)
	}
}

func TestNoVerdictsIsValid(t *testing.T) {
	rev, err := parseReview([]byte(`{"overview":"o","files":[],"comments":[],"verdicts":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Verdicts) != 0 {
		t.Errorf("verdicts = %v, want none", rev.Verdicts)
	}
}

// The number is the coverage pane's answer. What a number cannot say is which
// line, and that is the difference between "coverage went down" and "the error
// path you just added is the one nothing runs".
func TestUncoveredAddedLinesReachTheReviewer(t *testing.T) {
	in := Input{
		Change: &change.Set{Files: []change.File{{
			Path: "internal/store/user.go",
			Diff: "@@ -40,0 +41,5 @@\n+\tif err != nil {\n+\t\treturn err\n+\t}\n+\treturn nil\n+}\n",
		}}},
		LineCoverage: map[string]map[int]bool{
			"internal/store/user.go": {41: false, 42: false, 43: false, 44: true, 45: true},
		},
	}
	got := in.coverageSection()
	if !strings.Contains(got, "internal/store/user.go: 41-43") {
		t.Errorf("the uncovered added lines are not named as a range:\n%s", got)
	}
	if strings.Contains(got, "44") {
		t.Errorf("a covered line was reported as uncovered:\n%s", got)
	}
}

// An old uncovered line in a file this change touched is not this change's
// news, and reporting it invites a finding about code the author did not write.
func TestOnlyTheChangesOwnLinesAreReported(t *testing.T) {
	in := Input{
		Change: &change.Set{Files: []change.File{{
			Path: "internal/store/user.go",
			Diff: "@@ -40,0 +41,1 @@\n+\treturn nil\n",
		}}},
		LineCoverage: map[string]map[int]bool{
			"internal/store/user.go": {9: false, 41: true},
		},
	}
	if got := in.coverageSection(); got != "" {
		t.Errorf("reported lines the change did not add:\n%s", got)
	}
}

// No profile is the common case and not an error, so it produces no section
// rather than a heading saying nothing was found.
func TestNoProfileMeansNoSection(t *testing.T) {
	in := Input{Change: &change.Set{Files: []change.File{{Path: "a.go", Diff: "@@ -1,0 +1,1 @@\n+x\n"}}}}
	if got := in.coverageSection(); got != "" {
		t.Errorf("section = %q, want nothing", got)
	}
}

func TestLineRanges(t *testing.T) {
	for _, tc := range []struct {
		in   []int
		want string
	}{
		{[]int{41, 42, 43}, "41-43"},
		{[]int{41, 43}, "41, 43"},
		{[]int{41}, "41"},
		{[]int{1, 2, 5, 6, 7, 9}, "1-2, 5-7, 9"},
	} {
		if got := lineRanges(tc.in); got != tc.want {
			t.Errorf("lineRanges(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The section is priced with the facts rather than the context, so it is never
// truncated to fit. On a change that rewrites a package every added line is
// uncovered until the tests land, and unbounded that takes the ceiling from
// the context block.
func TestTheCoverageSectionIsBoundedAndSaysWhatItCut(t *testing.T) {
	files := make([]change.File, 0, maxCoverageFiles+3)
	lc := map[string]map[int]bool{}
	for i := 0; i < maxCoverageFiles+3; i++ {
		path := fmt.Sprintf("internal/pkg%d/x.go", i)
		var diff strings.Builder
		lines := map[int]bool{}
		fmt.Fprintf(&diff, "@@ -0,0 +1,%d @@\n", maxCoverageRanges*3)
		for line := 1; line <= maxCoverageRanges*3; line++ {
			diff.WriteString("+x\n")
			// Every other line, so each is its own range rather than one span.
			lines[line] = line%2 == 0
		}
		files = append(files, change.File{Path: path, Diff: diff.String()})
		lc[path] = lines
	}
	got := Input{Change: &change.Set{Files: files}, LineCoverage: lc}.coverageSection()

	if n := strings.Count(got, "internal/pkg"); n != maxCoverageFiles {
		t.Errorf("named %d files, want the cap of %d", n, maxCoverageFiles)
	}
	if !strings.Contains(got, "and 3 more file(s)") {
		t.Errorf("the files it cut are not counted:\n%s", got)
	}
	if !strings.Contains(got, "more line(s)") {
		t.Errorf("the lines it cut are not counted:\n%s", got)
	}
	first := strings.SplitN(got[strings.Index(got, "- internal/pkg0"):], "\n", 2)[0]
	if n := strings.Count(first, ","); n > maxCoverageRanges {
		t.Errorf("a file listed %d ranges, want at most %d:\n%s", n+1, maxCoverageRanges, first)
	}
}

// An error path nothing exercises is the case that fails in production and
// not in CI, so it is worth pointing at rather than leaving in a list of line
// numbers.
func TestUncoveredErrorHandlingIsCalledOut(t *testing.T) {
	in := Input{
		Change: &change.Set{Files: []change.File{{
			Path: "internal/store/user.go",
			Diff: "@@ -40,0 +41,4 @@\n+\tif err != nil {\n+\t\treturn fmt.Errorf(\"insert: %w\", err)\n+\t}\n+\ttotal++\n",
		}}},
		LineCoverage: map[string]map[int]bool{
			"internal/store/user.go": {41: false, 42: false, 43: false, 44: false},
		},
	}
	got := in.coverageSection()
	if !strings.Contains(got, "41-44") {
		t.Errorf("the uncovered lines are not listed:\n%s", got)
	}
	if !strings.Contains(got, "error handling: 41-42") {
		t.Errorf("the error path is not called out:\n%s", got)
	}
	if !strings.Contains(got, "fails in production and not in CI") {
		t.Errorf("the section does not say why that matters:\n%s", got)
	}
}

func TestHandlesError(t *testing.T) {
	for line, want := range map[string]bool{
		"\tif err != nil {":                     true,
		"\t\treturn fmt.Errorf(\"x: %w\", err)": true,
		"\t\treturn nil, err":                   true,
		"\tpanic(\"unreachable\")":              true,
		"  } catch (e) {":                       true,
		"    raise ValueError(msg)":             true,
		"\ttotal++":                             false,
		"\treturn nil":                          false,
		"":                                      false,
		"// returns an error when the row is missing": false,
	} {
		if got := handlesError(line); got != want {
			t.Errorf("handlesError(%q) = %v, want %v", line, got, want)
		}
	}
}

// The line numbers have to line up with the file, or the reviewer is pointed
// at the wrong code.
func TestAddedLineTextTracksHunkOffsets(t *testing.T) {
	got := addedLineText("@@ -1,2 +1,3 @@\n unchanged\n+added at 2\n context\n@@ -40,0 +41,1 @@\n+added at 41\n")
	if got[2] != "added at 2" {
		t.Errorf("line 2 = %q", got[2])
	}
	if got[41] != "added at 41" {
		t.Errorf("line 41 = %q; the second hunk's offset was not read", got[41])
	}
	if len(got) != 2 {
		t.Errorf("got %d added lines, want 2: %v", len(got), got)
	}
}
