package globmatch

import "testing"

func TestMatchesAny(t *testing.T) {
	patterns := []string{"**/*.go", "ui/**"}
	cases := []struct {
		path string
		want bool
	}{
		{"internal/foo.go", true},
		{"ui/src/App.tsx", true},
		{"README.md", false},
	}
	for _, tc := range cases {
		if got := MatchesAny(patterns, tc.path); got != tc.want {
			t.Errorf("MatchesAny(%q, %q) = %v, want %v", patterns, tc.path, got, tc.want)
		}
	}
}
