package report

import (
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

// FuzzFileWalkPathsStayOpaque fuzzes fileWalk with adversarial paths:
// traversal sequences, absolute paths, embedded newlines, and unicode.
//
// Invariant chosen after reading walk.go: fileWalk never resolves, cleans,
// or joins a change.File's Path against any base directory — it is pure
// data plumbing that copies Path into fileWalkRow.Path and correlates
// findings by exact string equality. That is what keeps a path like
// "../../../etc/passwd" or an absolute path inert on the report: since the
// walkthrough never joins the string against the report's own output
// directory, it can never be used to make the renderer address anything
// outside that directory. So the property to hold is that fileWalk's output
// path is byte-identical to its input, for any input, and that finding
// correlation still keys off the identical (never normalized) string. A
// version of fileWalk that started calling filepath.Clean or filepath.Join
// on Path would violate this and would be the point where such a path could
// stop being inert.
func FuzzFileWalkPathsStayOpaque(f *testing.F) {
	seeds := []string{
		"a.go",
		"../../../etc/passwd",
		"/etc/passwd",
		"..\\..\\windows\\system32\\config",
		"a/../../b.go",
		"weird\nname.go",
		"ウェブ/コンポーネント.tsx",
		"",
		"....//....//etc/passwd",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, path string) {
		files := []change.File{{Path: path, Status: "modified", Added: 1}}
		fs := []findings.Finding{{File: path, Severity: findings.SeverityError}}

		rows := fileWalk(files, fs)

		if len(rows) != 1 {
			t.Fatalf("fileWalk must return one row per input file, got %d", len(rows))
		}
		if rows[0].Path != path {
			t.Fatalf("fileWalk must carry the path through byte-identical (never join/clean/resolve it): got %q, want %q", rows[0].Path, path)
		}
		// fileWalk deliberately skips findings with an empty File (they have
		// no file to attach to); every other literal path, however
		// adversarial, must still correlate by exact string match.
		wantFindings := 1
		if path == "" {
			wantFindings = 0
		}
		if rows[0].Findings != wantFindings {
			t.Fatalf("a finding whose File matches the literal path string must attach to its row: got %d, want %d", rows[0].Findings, wantFindings)
		}
	})
}
