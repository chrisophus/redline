package findings

import (
	"fmt"
	"strings"
	"testing"
)

// TestReviewKey verifies that ReviewKey generates consistent hashes for
// the same defect worded differently.
func TestReviewKey(t *testing.T) {
	tests := []struct {
		name     string
		f1, f2   Finding
		wantSame bool
		desc     string
	}{
		{
			name:     "same file line message",
			wantSame: true,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Cursor",
			},
			desc: "identical messages should hash the same",
		},
		{
			name:     "different wording same issue",
			wantSame: true,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check on function call with context and information for debugging purposes in production",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check on function call with context and information for debugging purposes in development",
				Reviewer: "Cursor",
			},
			desc: "messages with identical first 12 words should hash the same",
		},
		{
			name:     "case insensitive",
			wantSame: true,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing Error Check",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "missing error check",
				Reviewer: "Cursor",
			},
			desc: "case should be normalized",
		},
		{
			name:     "punctuation ignored",
			wantSame: true,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check!",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check.",
				Reviewer: "Cursor",
			},
			desc: "punctuation should be stripped",
		},
		{
			name:     "different files",
			wantSame: false,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "other.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Cursor",
			},
			desc: "different files should have different keys",
		},
		{
			name:     "different lines",
			wantSame: false,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     43,
				Message:  "Missing error check",
				Reviewer: "Cursor",
			},
			desc: "different lines should have different keys",
		},
		{
			name:     "different essential message",
			wantSame: false,
			f1: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Missing error check",
				Reviewer: "Claude",
			},
			f2: Finding{
				File:     "main.go",
				Line:     42,
				Message:  "Unused variable",
				Reviewer: "Cursor",
			},
			desc: "completely different messages should have different keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key1 := ReviewKey(tt.f1)
			key2 := ReviewKey(tt.f2)

			if tt.wantSame && key1 != key2 {
				t.Errorf("ReviewKey mismatch: %q != %q (%s)", key1, key2, tt.desc)
			}
			if !tt.wantSame && key1 == key2 {
				t.Errorf("ReviewKey should differ: both %q (%s)", key1, tt.desc)
			}
		})
	}
}

// TestMergeSameDefect verifies that findings with the same ReviewKey
// collapse into one with both reporters recorded.
func TestMergeSameDefect(t *testing.T) {
	findings := []Finding{
		{
			File:       "main.go",
			Line:       42,
			Rule:       "missing-error-check",
			Substrate:  "Go",
			Category:   "error",
			Severity:   SeverityError,
			Message:    "Missing error check on function call result and context information for debugging purposes in production",
			Reviewer:   "Claude",
			Confidence: "high",
		},
		{
			File:       "main.go",
			Line:       42,
			Rule:       "missing-error-check",
			Substrate:  "Go",
			Category:   "error",
			Severity:   SeverityError,
			Message:    "Missing error check on function call result and context information for debugging purposes in development",
			Reviewer:   "Cursor",
			Confidence: "medium",
		},
	}

	result := Merge(findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding after merge, got %d", len(result))
	}

	f := result[0]

	// Should keep the longer message (both are same length, but Claude's message is "longer" in the sense of being alphabetically later)
	// Actually both are same length, just ensure we keep one
	if !strings.Contains(f.Message, "Missing error check on function call result and context information for debugging purposes in") {
		t.Errorf("expected message starting with correct prefix, got %q", f.Message)
	}

	// Should have both reporters
	if len(f.Reporters) != 2 {
		t.Fatalf("expected 2 reporters, got %d: %v", len(f.Reporters), f.Reporters)
	}

	// Reporters should be sorted
	if f.Reporters[0] != "Claude" || f.Reporters[1] != "Cursor" {
		t.Errorf("reporters not sorted: %v", f.Reporters)
	}
}

// TestMergeDifferentLines verifies that findings at different lines
// do NOT collapse.
func TestMergeDifferentLines(t *testing.T) {
	findings := []Finding{
		{
			File:      "main.go",
			Line:      42,
			Rule:      "missing-error-check",
			Substrate: "Go",
			Category:  "error",
			Severity:  SeverityError,
			Message:   "Missing error check",
			Reviewer:  "Claude",
		},
		{
			File:      "main.go",
			Line:      43,
			Rule:      "missing-error-check",
			Substrate: "Go",
			Category:  "error",
			Severity:  SeverityError,
			Message:   "Missing error check",
			Reviewer:  "Cursor",
		},
	}

	result := Merge(findings)

	if len(result) != 2 {
		t.Fatalf("expected 2 findings after merge, got %d", len(result))
	}

	if result[0].Line == result[1].Line {
		t.Errorf("findings at different lines should not collapse")
	}
}

// TestMergeSeverityPromotion verifies that the highest severity is kept.
func TestMergeSeverityPromotion(t *testing.T) {
	findings := []Finding{
		{
			File:      "main.go",
			Line:      42,
			Rule:      "issue",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityInfo,
			Message:   "Something changed",
			Reviewer:  "Claude",
		},
		{
			File:      "main.go",
			Line:      42,
			Rule:      "issue",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Something changed",
			Reviewer:  "Cursor",
		},
		{
			File:      "main.go",
			Line:      42,
			Rule:      "issue",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityWarning,
			Message:   "Something changed",
			Reviewer:  "Copilot",
		},
	}

	result := Merge(findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	if result[0].Severity != SeverityError {
		t.Errorf("expected Error severity, got %v", result[0].Severity)
	}

	if len(result[0].Reporters) != 3 {
		t.Fatalf("expected 3 reporters, got %d", len(result[0].Reporters))
	}
}

// TestMergeDeterminism verifies that Merge produces identical output
// regardless of input order.
func TestMergeDeterminism(t *testing.T) {
	baseFindings := []Finding{
		{
			File:      "a.go",
			Line:      10,
			Rule:      "rule1",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "First issue",
			Reviewer:  "Claude",
		},
		{
			File:      "b.go",
			Line:      20,
			Rule:      "rule2",
			Substrate: "Go",
			Category:  "schema",
			Severity:  SeverityWarning,
			Message:   "Second issue",
			Reviewer:  "Cursor",
		},
		{
			File:      "a.go",
			Line:      10,
			Rule:      "rule1",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityWarning,
			Message:   "First issue (different wording)",
			Reviewer:  "Copilot",
		},
	}

	// Run Merge several times with different orderings
	results := make([][]Finding, 3)

	// Order 1: original
	results[0] = Merge(baseFindings)

	// Order 2: reversed
	reversed := make([]Finding, len(baseFindings))
	copy(reversed, baseFindings)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	results[1] = Merge(reversed)

	// Order 3: rotated
	rotated := make([]Finding, len(baseFindings))
	copy(rotated, baseFindings)
	if len(rotated) > 0 {
		first := rotated[0]
		copy(rotated, rotated[1:])
		rotated[len(rotated)-1] = first
	}
	results[2] = Merge(rotated)

	// All results should be identical
	for i := 1; i < len(results); i++ {
		if len(results[i]) != len(results[0]) {
			t.Errorf("result %d has different length: %d vs %d", i, len(results[i]), len(results[0]))
			continue
		}

		for j := range results[0] {
			if results[i][j].File != results[0][j].File ||
				results[i][j].Line != results[0][j].Line ||
				results[i][j].Severity != results[0][j].Severity ||
				len(results[i][j].Reporters) != len(results[0][j].Reporters) {
				t.Errorf("result %d differs from result 0 at index %d", i, j)
			}
		}
	}
}

// TestRankOrdering verifies that Rank sorts findings correctly for human reading.
func TestRankOrdering(t *testing.T) {
	tests := []struct {
		name     string
		findings []Finding
		checkFn  func(t *testing.T, ranked []Finding)
		desc     string
	}{
		{
			name: "reporters first",
			findings: []Finding{
				{
					File:      "a.go",
					Line:      1,
					Severity:  SeverityInfo,
					Reporters: []string{"Claude"},
					Message:   "Issue with one reporter",
				},
				{
					File:      "b.go",
					Line:      1,
					Severity:  SeverityError,
					Reporters: []string{"Claude", "Cursor"},
					Message:   "Issue with two reporters",
				},
				{
					File:      "c.go",
					Line:      1,
					Severity:  SeverityError,
					Reporters: []string{},
					Message:   "Issue with no reporters",
				},
			},
			checkFn: func(t *testing.T, ranked []Finding) {
				// Should be ordered: 2 reporters, then 1, then 0
				if len(ranked[0].Reporters) != 2 {
					t.Errorf("first should have 2 reporters, got %d", len(ranked[0].Reporters))
				}
				if len(ranked[1].Reporters) != 1 {
					t.Errorf("second should have 1 reporter, got %d", len(ranked[1].Reporters))
				}
				if len(ranked[2].Reporters) != 0 {
					t.Errorf("third should have 0 reporters, got %d", len(ranked[2].Reporters))
				}
			},
			desc: "more reporters come first",
		},
		{
			name: "severity within reporters",
			findings: []Finding{
				{
					File:      "a.go",
					Line:      1,
					Severity:  SeverityInfo,
					Reporters: []string{"Claude", "Cursor"},
					Message:   "Info with 2 reporters",
				},
				{
					File:      "b.go",
					Line:      1,
					Severity:  SeverityError,
					Reporters: []string{"Claude", "Cursor"},
					Message:   "Error with 2 reporters",
				},
			},
			checkFn: func(t *testing.T, ranked []Finding) {
				// Error should come first (higher priority)
				if ranked[0].Severity != SeverityError {
					t.Errorf("first should be Error, got %v", ranked[0].Severity)
				}
			},
			desc: "error severity comes before info",
		},
		{
			name: "confidence with same reporters and severity",
			findings: []Finding{
				{
					File:       "a.go",
					Line:       1,
					Severity:   SeverityError,
					Reporters:  []string{"Claude"},
					Confidence: "low",
					Message:    "Low confidence",
				},
				{
					File:       "b.go",
					Line:       1,
					Severity:   SeverityError,
					Reporters:  []string{"Claude"},
					Confidence: "high",
					Message:    "High confidence",
				},
				{
					File:       "c.go",
					Line:       1,
					Severity:   SeverityError,
					Reporters:  []string{"Claude"},
					Confidence: "",
					Message:    "No confidence",
				},
			},
			checkFn: func(t *testing.T, ranked []Finding) {
				// Should be ordered: high, low, empty
				if ranked[0].Confidence != "high" {
					t.Errorf("first should have high confidence, got %q", ranked[0].Confidence)
				}
				if ranked[1].Confidence != "low" {
					t.Errorf("second should have low confidence, got %q", ranked[1].Confidence)
				}
				if ranked[2].Confidence != "" {
					t.Errorf("third should have empty confidence, got %q", ranked[2].Confidence)
				}
			},
			desc: "high confidence comes before low",
		},
		{
			name: "file then line for same everything else",
			findings: []Finding{
				{
					File:      "b.go",
					Line:      1,
					Severity:  SeverityError,
					Reporters: []string{"Claude"},
					Message:   "Issue",
				},
				{
					File:      "a.go",
					Line:      2,
					Severity:  SeverityError,
					Reporters: []string{"Claude"},
					Message:   "Issue",
				},
				{
					File:      "a.go",
					Line:      1,
					Severity:  SeverityError,
					Reporters: []string{"Claude"},
					Message:   "Issue",
				},
			},
			checkFn: func(t *testing.T, ranked []Finding) {
				// Should be ordered: a.go:1, a.go:2, b.go:1
				if ranked[0].File != "a.go" || ranked[0].Line != 1 {
					t.Errorf("first should be a.go:1, got %s:%d", ranked[0].File, ranked[0].Line)
				}
				if ranked[1].File != "a.go" || ranked[1].Line != 2 {
					t.Errorf("second should be a.go:2, got %s:%d", ranked[1].File, ranked[1].Line)
				}
				if ranked[2].File != "b.go" || ranked[2].Line != 1 {
					t.Errorf("third should be b.go:1, got %s:%d", ranked[2].File, ranked[2].Line)
				}
			},
			desc: "file and line ordering as fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranked := Rank(tt.findings)
			tt.checkFn(t, ranked)
		})
	}
}

// TestMergeMultipleGroups verifies that Merge can accept multiple Finding slices.
func TestMergeMultipleGroups(t *testing.T) {
	group1 := []Finding{
		{
			File:      "main.go",
			Line:      10,
			Rule:      "rule1",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "First issue with some additional context in production environment for debugging purposes and more details",
			Reviewer:  "Claude",
		},
	}

	group2 := []Finding{
		{
			File:      "main.go",
			Line:      10,
			Rule:      "rule1",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityWarning,
			Message:   "First issue with some additional context in production environment for debugging purposes in another context",
			Reviewer:  "Cursor",
		},
	}

	result := Merge(group1, group2)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	if len(result[0].Reporters) != 2 {
		t.Fatalf("expected 2 reporters, got %d", len(result[0].Reporters))
	}

	// Severity should be promoted to Error
	if result[0].Severity != SeverityError {
		t.Errorf("expected Error severity, got %v", result[0].Severity)
	}
}

// TestMergeLongerMessage verifies that the longer message is kept.
func TestMergeLongerMessage(t *testing.T) {
	findings := []Finding{
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Error in parsing configuration file with extra debugging information context and details for production environment",
			Reviewer:  "Claude",
		},
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Error in parsing configuration file with extra debugging information context and details for development environment",
			Reviewer:  "Cursor",
		},
	}

	result := Merge(findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	// Both messages are same length after first 12 words are truncated.
	// The longer original message should be kept.
	// "...development environment" is longer than "...production environment"
	if result[0].Message != "Error in parsing configuration file with extra debugging information context and details for development environment" {
		t.Errorf("expected message with 'development environment', got %q", result[0].Message)
	}
}

// TestReportersDedup verifies that duplicate reporters are removed.
func TestReportersDedup(t *testing.T) {
	findings := []Finding{
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Issue",
			Reviewer:  "Claude",
		},
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Issue",
			Reviewer:  "Claude",
		},
	}

	result := Merge(findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	if len(result[0].Reporters) != 1 {
		t.Fatalf("expected 1 reporter after dedup, got %d: %v", len(result[0].Reporters), result[0].Reporters)
	}

	if result[0].Reporters[0] != "Claude" {
		t.Errorf("expected Claude, got %q", result[0].Reporters[0])
	}
}

// TestMergeRedlineFindings verifies that Redline's deterministic findings
// (with empty Reviewer) work correctly.
func TestMergeRedlineFindings(t *testing.T) {
	findings := []Finding{
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Redline finding",
			Reviewer:  "", // Redline's own finding
		},
		{
			File:      "main.go",
			Line:      1,
			Rule:      "rule",
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   "Redline finding",
			Reviewer:  "Claude",
		},
	}

	result := Merge(findings)

	if len(result) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(result))
	}

	// Reporters should only have Claude (empty Reviewer is not added)
	if len(result[0].Reporters) != 1 || result[0].Reporters[0] != "Claude" {
		t.Errorf("expected [Claude], got %v", result[0].Reporters)
	}
}

// BenchmarkReviewKey benchmarks the ReviewKey computation.
func BenchmarkReviewKey(b *testing.B) {
	f := Finding{
		File:     "internal/foo/bar.go",
		Line:     42,
		Message:  "This is a long message describing the issue that needs to be discovered",
		Reviewer: "Claude",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ReviewKey(f)
	}
}

// BenchmarkMerge benchmarks merging findings.
func BenchmarkMerge(b *testing.B) {
	findings := make([]Finding, 100)
	for i := 0; i < 100; i++ {
		findings[i] = Finding{
			File:      fmt.Sprintf("file%d.go", i%10),
			Line:      i%50 + 1,
			Rule:      fmt.Sprintf("rule%d", i%5),
			Substrate: "Go",
			Category:  "api",
			Severity:  SeverityError,
			Message:   fmt.Sprintf("Issue number %d", i),
			Reviewer:  []string{"Claude", "Cursor", "Copilot"}[i%3],
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Merge(findings)
	}
}

// BenchmarkRank benchmarks the Rank sorting.
func BenchmarkRank(b *testing.B) {
	findings := make([]Finding, 100)
	for i := 0; i < 100; i++ {
		findings[i] = Finding{
			File:       fmt.Sprintf("file%d.go", i%10),
			Line:       i%50 + 1,
			Rule:       fmt.Sprintf("rule%d", i%5),
			Substrate:  "Go",
			Category:   "api",
			Severity:   []Severity{SeverityError, SeverityWarning, SeverityInfo}[i%3],
			Message:    fmt.Sprintf("Issue number %d", i),
			Confidence: []string{"high", "medium", "low"}[i%3],
			Reporters:  []string{"Claude", "Cursor"}[:i%2+1],
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Rank(findings)
	}
}
