package findings_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/cover"
	"github.com/ccason/redline/internal/findings"
)

func TestFinalizeDefaultsSeverityFromCategory(t *testing.T) {
	cases := []struct {
		category findings.Category
		want     findings.Severity
	}{
		{findings.CategorySchema, findings.SeverityError},
		{findings.CategoryContract, findings.SeverityError},
		{findings.CategoryCover, findings.SeverityWarning},
		{findings.CategoryUI, findings.SeverityWarning},
		{findings.CategoryLint, findings.SeverityWarning},
	}
	for _, c := range cases {
		rep := findings.Report{Findings: []findings.Finding{{Category: c.category}}}
		rep.Finalize()
		if got := rep.Findings[0].Severity; got != c.want {
			t.Errorf("%s: got severity %q, want %q", c.category, got, c.want)
		}
	}
}

func TestFinalizeKeepsExplicitSeverity(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategorySchema, Severity: findings.SeverityInfo},
	}}
	rep.Finalize()
	if rep.Findings[0].Severity != findings.SeverityInfo {
		t.Fatalf("Finalize must not override an explicit severity, got %q", rep.Findings[0].Severity)
	}
}

func TestFinalizeStampsSourceNewAndFingerprint(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategoryLint, Rule: "r", Message: "m", File: "a.go"},
	}}
	rep.Finalize()
	got := rep.Findings[0]
	if got.Source != findings.SourceDeterministic {
		t.Errorf("Source should default to deterministic, got %q", got.Source)
	}
	if !got.New {
		t.Error("every Redline pane is diff-based; New must always be true")
	}
	want := findings.Fingerprint(findings.Finding{File: "a.go", Rule: "r", Message: "m"})
	if got.Fingerprint != want {
		t.Errorf("stamped fingerprint %q does not match Fingerprint(f) %q", got.Fingerprint, want)
	}
}

// NewCount is the number a reader trusts to know how many findings of each
// severity landed, without counting the list themselves.
func TestFinalizeNewCountAggregation(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategorySchema},                              // -> error
		{Category: findings.CategorySchema},                              // -> error
		{Category: findings.CategoryUI, Severity: findings.SeverityInfo}, // explicit
		{Category: findings.CategoryLint},                                // -> warning
	}}
	rep.Finalize()
	want := map[findings.Severity]int{
		findings.SeverityError:   2,
		findings.SeverityInfo:    1,
		findings.SeverityWarning: 1,
	}
	if len(rep.NewCount) != len(want) {
		t.Fatalf("NewCount = %+v, want %+v", rep.NewCount, want)
	}
	for sev, n := range want {
		if rep.NewCount[sev] != n {
			t.Errorf("NewCount[%s] = %d, want %d", sev, rep.NewCount[sev], n)
		}
	}
}

func TestFinalizeNilFindingsAndSubstratesMarshalAsEmptyArrays(t *testing.T) {
	rep := findings.Report{}
	rep.Finalize()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"findings":null`) || strings.Contains(string(b), `"substrates":null`) {
		t.Fatalf("Finalize must leave findings/substrates JSON-encodable as [], not null: %s", b)
	}
}

func TestFailedSubstratesSelectsFailedOnly(t *testing.T) {
	rep := findings.Report{Substrates: []findings.SubstrateStatus{
		{Name: "a", State: findings.SubstrateRan},
		{Name: "b", State: findings.SubstrateSkipped},
		{Name: "c", State: findings.SubstrateFailed, Detail: "panic"},
	}}
	failed := rep.FailedSubstrates()
	if len(failed) != 1 || failed[0].Name != "c" {
		t.Fatalf("FailedSubstrates should return only the failed pane, got %+v", failed)
	}
}

// The skill that emits findings.json and the merge gate that reads it back
// depend on these exact field names; a rename here is a silent wire-format
// break neither side would compile-catch.
func TestReportJSONFieldNamesMatchTheWireContract(t *testing.T) {
	rep := findings.Report{
		Substrates:    []findings.SubstrateStatus{{Name: "s", State: findings.SubstrateRan}},
		Confirmations: []findings.Confirmation{{Substrate: "s", Rule: "r", Message: "ok"}},
		Unknowns:      []findings.Unknown{{Substrate: "s", Message: "could not tell"}},
		Coverage:      findings.Coverage{Diff: &cover.Result{Profile: "coverage.out", Lines: 10, Covered: 5, Percent: 50}},
	}
	rep.Finalize()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, field := range []string{`"unknowns"`, `"confirmations"`, `"substrates"`, `"diffCoverage"`} {
		if !strings.Contains(body, field) {
			t.Errorf("Report JSON must carry field %s; the skill/merge gate reads it by name: %s", field, body)
		}
	}
}

func TestLoadReviewMergesByFingerprint(t *testing.T) {
	dir := t.TempDir()
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategoryLint, Rule: "suppression-added", Message: "adds a nolint", File: "a.go"},
	}}
	rep.Finalize()
	fp := rep.Findings[0].Fingerprint
	path := filepath.Join(dir, "review.json")
	// A real agent writes review.json with a JSON encoder, so the fingerprint's
	// NUL separators round-trip as \u0000. The file claims source
	// "deterministic"; Redline must override it to llm on load.
	data, err := json.Marshal(findings.Review{Verdicts: map[string]findings.Verdict{
		fp: {Ruling: "justified", Rationale: "fixture", Source: findings.SourceDeterministic},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := findings.LoadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	rep.MergeVerdicts(v.Verdicts)
	got := rep.Findings[0].Verdict
	if got == nil {
		t.Fatal("a verdict keyed on the finding's fingerprint must attach")
	}
	if got.Ruling != "justified" || got.Rationale != "fixture" {
		t.Fatalf("verdict = %+v", got)
	}
	if got.Source != findings.SourceLLM {
		t.Errorf("verdict source = %q, want llm; Redline attributes the reading, the file does not", got.Source)
	}
}

func TestLoadReviewMissingFileIsNotAnError(t *testing.T) {
	v, err := findings.LoadReview(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || v != nil {
		t.Fatalf("a missing review file must read as (nil, nil), got %v / %v", v, err)
	}
}

func TestMergeVerdictsDropsUnmatchedFingerprint(t *testing.T) {
	rep := findings.Report{Findings: []findings.Finding{
		{Category: findings.CategoryLint, Rule: "r", Message: "m", File: "a.go"},
	}}
	rep.Finalize()
	rep.MergeVerdicts(map[string]findings.Verdict{"no-such-fingerprint": {Ruling: "justified"}})
	if rep.Findings[0].Verdict != nil {
		t.Error("a verdict matching no finding must be dropped, not attached to the wrong one")
	}
}

// The agent's line comments come in as review comments and leave as findings so
// they render on the diff line beside Redline's own. Source is always llm, an
// empty body is dropped, and a comment with no severity defaults to info.
func TestReviewCommentsBecomeFindings(t *testing.T) {
	r := &findings.Review{Comments: []findings.ReviewComment{
		{File: "a.go", Line: 12, Body: "swallows the error", Severity: findings.SeverityWarning},
		{File: "b.go", Line: 3, Body: "reads fine"},
		{File: "c.go", Line: 9, Body: ""},
	}}
	fs := r.CommentFindings()
	if len(fs) != 2 {
		t.Fatalf("an empty-body comment must be dropped; got %d findings", len(fs))
	}
	if fs[0].Source != findings.SourceLLM || fs[0].Rule != "agent-comment" {
		t.Errorf("a comment must become an llm agent-comment finding: %+v", fs[0])
	}
	if fs[0].Severity != findings.SeverityWarning {
		t.Errorf("a comment's severity must carry through, got %q", fs[0].Severity)
	}
	if fs[1].Severity != findings.SeverityInfo {
		t.Errorf("a comment with no severity defaults to info, got %q", fs[1].Severity)
	}
}

// review.json is written by an agent, and agents write "Warning" or "high"
// where the schema says "warning". An unvalidated severity would outrank real
// errors in Sort, appear in no severity tile, and never match the post gate's
// blocking list, so anything but the three known values must normalize.
func TestCommentFindingsNormalizesSeverity(t *testing.T) {
	r := &findings.Review{Comments: []findings.ReviewComment{
		{File: "a.go", Line: 1, Severity: "Warning", Body: "capitalized"},
		{File: "a.go", Line: 2, Severity: "high", Body: "unknown value"},
		{File: "a.go", Line: 3, Body: "empty"},
	}}
	got := r.CommentFindings()
	want := []findings.Severity{findings.SeverityWarning, findings.SeverityInfo, findings.SeverityInfo}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Severity != want[i] {
			t.Errorf("comment %d: severity %q, want %q", i, got[i].Severity, want[i])
		}
	}
}
