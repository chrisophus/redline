package change

import "testing"

func TestMatchExclude(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// A bare name is about the file, at any depth.
		{"*.snap", "ui/src/__snapshots__/a.snap", true},
		{"*.snap", "a.snap", true},
		{"*.snap", "ui/src/a.snapshot", false},
		// A directory takes everything under it.
		{"internal/oas/", "internal/oas/oas_client_gen.go", true},
		{"internal/oas/", "internal/oas/deep/er/x.go", true},
		{"internal/oas/", "internal/oastwo/x.go", false},
		// ** crosses segments and also matches none of them.
		{"**/oas_*.go", "internal/api/oas_schemas_gen.go", true},
		{"**/oas_*.go", "oas_schemas_gen.go", true},
		{"api/**/gen.go", "api/gen.go", true},
		{"api/**/gen.go", "api/v1/inner/gen.go", true},
		{"api/**/gen.go", "other/api/gen.go", false},
		// * stays inside one segment.
		{"api/*.go", "api/client.go", true},
		{"api/*.go", "api/v1/client.go", false},
		{"?.go", "a.go", true},
		{"?.go", "ab.go", false},
		// Anchored at both ends, so a pattern is not a substring test.
		{"gen", "gen/x.go", false},
		{"gen/**", "gen/x.go", true},
		// Case-sensitive, because git is.
		{"*.GO", "main.go", false},
		// Regex metacharacters in a pattern are literal, so a file really
		// named [ is matched by the pattern [ and by nothing else.
		{"[", "[", true},
		{"[", "a.go", false},
		{"a.go", "axgo", false},
		{"", "a.go", false},
		{"*.go", "", false},
	}
	for _, c := range cases {
		if got := matchExclude(c.path, c.pattern); got != c.want {
			t.Errorf("matchExclude(%q, %q) = %v, want %v", c.path, c.pattern, got, c.want)
		}
	}
}

// The reason carries the pattern, because a reader who finds a file missing
// from a review needs to know which line of the config removed it.
func TestExcludeReasonNamesThePattern(t *testing.T) {
	patterns := []string{"**/*.pb.go", "internal/oas/"}
	got := ExcludeReason("internal/oas/oas_client_gen.go", patterns)
	if got != "excluded by .redline.yml: internal/oas/" {
		t.Errorf("reason = %q, want the pattern that matched", got)
	}
	if r := ExcludeReason("internal/api/client.go", patterns); r != "" {
		t.Errorf("unmatched path got reason %q, want none", r)
	}
	if r := ExcludeReason("internal/api/client.go", nil); r != "" {
		t.Errorf("no patterns got reason %q, want none", r)
	}
}
