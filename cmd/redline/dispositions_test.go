package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/dispositions"
)

// fakeGH answers gh and git by command-line prefix.
type fakeGH struct {
	answers map[string]string
	errs    map[string]error
}

func (f fakeGH) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	for prefix, err := range f.errs {
		if strings.HasPrefix(line, prefix) {
			return nil, err
		}
	}
	for prefix, out := range f.answers {
		if strings.HasPrefix(line, prefix) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

// onePR is a merged PR with one resolved thread a person replied to.
const onePR = `{"data":{"repository":{"pullRequest":{
 "number":7,"state":"MERGED","createdAt":"2026-11-01T10:00:00Z","closedAt":"2026-11-02T10:00:00Z","mergedAt":"2026-11-02T11:00:00Z",
 "author":{"__typename":"User"},
 "commits":{"totalCount":1,"nodes":[{"commit":{"oid":"abc1234","committedDate":"2026-11-01T10:00:00Z","parents":{"totalCount":1}}}]},
 "reviewThreads":{"totalCount":1,"nodes":[{"isResolved":true,"isOutdated":false,"path":"a.go","line":10,"originalLine":9,"comments":{"nodes":[{"body":"a nil map","createdAt":"2026-11-01T11:00:00Z","author":{"__typename":"Bot"},"originalCommit":{"oid":"abc1234"}},{"body":"**Not fixing (intentional).** checked","createdAt":"2026-11-01T11:05:00Z","author":{"__typename":"User"}}]}}]},
 "reviews":{"totalCount":0,"nodes":[]},
 "comments":{"totalCount":1,"nodes":[{"body":"<!-- evidence -->\n| a | b | Verified |"}]}}}}}`

func TestDispositionPRs(t *testing.T) {
	ctx := context.Background()
	got, err := dispositionPRs(ctx, dispositions.GitHub{}, opts{pr: "3, 4"})
	if err != nil || !slices.Equal(got, []int{3, 4}) {
		t.Fatalf("dispositionPRs(3, 4) = %v, %v", got, err)
	}
	for _, bad := range []string{"x", "0", "-1", "1,,2"} {
		if _, err := dispositionPRs(ctx, dispositions.GitHub{}, opts{pr: bad}); err == nil {
			t.Errorf("--pr %q should be refused", bad)
		}
	}
	if _, err := dispositionPRs(ctx, dispositions.GitHub{}, opts{}); err == nil {
		t.Error("no PRs should be refused")
	}
	if _, err := dispositionPRs(ctx, dispositions.GitHub{}, opts{pr: "1", recent: 2}); err == nil {
		t.Error("--pr with --recent should be refused")
	}
}

func TestDispositionsRepo(t *testing.T) {
	ctx := context.Background()
	gh, err := dispositionsRepo(ctx, fakeGH{}, "o/n")
	if err != nil || gh.Owner != "o" || gh.Name != "n" {
		t.Fatalf("dispositionsRepo(o/n) = %+v, %v", gh, err)
	}
	gh, err = dispositionsRepo(ctx, fakeGH{answers: map[string]string{"gh repo view": "cur/repo\n"}}, "")
	if err != nil || gh.Owner != "cur" || gh.Name != "repo" {
		t.Fatalf("the current repository = %+v, %v", gh, err)
	}
	for _, bad := range []string{"noslash", "/n", "o/"} {
		if _, err := dispositionsRepo(ctx, fakeGH{}, bad); err == nil {
			t.Errorf("--repo %q should be refused", bad)
		}
	}
	boom := errors.New("not logged in")
	if _, err := dispositionsRepo(ctx, fakeGH{errs: map[string]error{"gh": boom}}, ""); !errors.Is(err, boom) {
		t.Errorf("a gh failure should surface, got %v", err)
	}
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestRunDispositions_WritesJSONL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	gh := fakeGH{answers: map[string]string{"gh api graphql": onePR}}
	var stdout, stderr strings.Builder
	o := opts{pr: "7", repo: "o/n", jsonl: dir, evidenceMarker: "<!-- evidence -->"}
	if err := runDispositions(context.Background(), o, gh, &stdout, &stderr); err != nil {
		t.Fatalf("runDispositions: %v (stderr %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PRs recorded: 1") {
		t.Errorf("summary missing: %s", stdout.String())
	}
	prs := readJSONL(t, filepath.Join(dir, dispositions.FilePRs))
	if len(prs) != 1 {
		t.Fatalf("prs.jsonl has %d rows, want 1", len(prs))
	}
	if prs[0]["HasEvidence"] != float64(1) {
		t.Errorf("the evidence marker should be read: %v", prs[0])
	}
	if n := len(readJSONL(t, filepath.Join(dir, dispositions.FileFindings))); n != 1 {
		t.Errorf("findings.jsonl has %d rows, want 1: another bot's thread is recorded too", n)
	}
	disps := readJSONL(t, filepath.Join(dir, dispositions.FileDispositions))
	if len(disps) != 1 || disps[0]["Disposition"] != string(dispositions.DispDeclined) {
		t.Errorf("dispositions.jsonl = %v, want one declined", disps)
	}
}

func TestRunDispositions_OneBadPRDoesNotHideTheRest(t *testing.T) {
	gh := fakeGH{answers: map[string]string{"gh api graphql": onePR}}
	bad := fakeGH{errs: map[string]error{"gh api graphql": errors.New("boom")}}
	var stdout, stderr strings.Builder
	if err := runDispositions(context.Background(), opts{pr: "7", repo: "o/n"}, gh, &stdout, &stderr); err != nil {
		t.Fatalf("a good PR: %v", err)
	}
	stdout.Reset()
	err := runDispositions(context.Background(), opts{pr: "7,8", repo: "o/n"}, bad, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("want 2 of 2 failed, got %v", err)
	}
	if !strings.Contains(stderr.String(), "PR 8") {
		t.Errorf("each failure is reported: %s", stderr.String())
	}
	if err := runDispositions(context.Background(), opts{pr: "7", repo: "o/n", artifacts: filepath.Join(t.TempDir(), "missing")}, gh, &stdout, &stderr); err == nil {
		t.Error("a missing --artifacts directory should fail")
	}
}
