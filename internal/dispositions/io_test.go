package dispositions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/findings"
)

// fakeRunner answers by command line, and records every call.
type fakeRunner struct {
	answers map[string]string
	errs    map[string]error
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
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

// errContains reports whether err is non-nil and its message contains sub.
func errContains(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}

const prJSON = `{"data":{"repository":{"pullRequest":{
 "number":7,"state":"MERGED","createdAt":"2026-11-01T10:00:00Z","closedAt":"2026-11-02T10:00:00Z","mergedAt":"2026-11-02T11:00:00Z",
 "author":{"__typename":"Bot"},
 "commits":{"totalCount":2,"nodes":[{"commit":{"oid":"ABC1234","committedDate":"2026-11-01T10:00:00Z","parents":{"totalCount":1}}},{"commit":{"oid":"def5678","committedDate":"2026-11-01T12:00:00Z","parents":{"totalCount":2}}}]},
 "reviewThreads":{"totalCount":1,"nodes":[{"isResolved":true,"isOutdated":false,"path":"a.go","line":10,"originalLine":9,"comments":{"nodes":[{"body":"b1","createdAt":"2026-11-01T11:00:00Z","author":{"__typename":"Bot"},"originalCommit":{"oid":"ABC1234"}},{"body":"b2","createdAt":"2026-11-01T11:05:00Z","author":{"__typename":"User"}}]}}]},
 "reviews":{"totalCount":1,"nodes":[{"body":"review"}]},
 "comments":{"totalCount":1,"nodes":[{"body":"c"}]}}}}}`

func TestParsePR(t *testing.T) {
	t.Parallel()
	pr, err := parsePR([]byte(prJSON), 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.State != "MERGED" || !pr.AuthorIsBot {
		t.Errorf("State, AuthorIsBot = %q, %v, want MERGED, true", pr.State, pr.AuthorIsBot)
	}
	if want := time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC); !pr.ClosedAt.Equal(want) {
		t.Errorf("ClosedAt = %v, want the merge time %v, which wins over the close time", pr.ClosedAt, want)
	}
	if len(pr.Commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(pr.Commits))
	}
	if pr.Commits[0].SHA != "abc1234" {
		t.Errorf("commit SHA = %q, want it lowered to abc1234", pr.Commits[0].SHA)
	}
	if !pr.Commits[1].IsMerge {
		t.Error("the second commit has two parents and should be a merge")
	}
	if len(pr.Threads) != 1 {
		t.Fatalf("got %d threads, want 1", len(pr.Threads))
	}
	th := pr.Threads[0]
	if th.OrigLine != 9 {
		t.Errorf("OrigLine = %d, want 9", th.OrigLine)
	}
	if !th.Comments[0].AuthorIsBot || th.Comments[1].AuthorIsBot {
		t.Errorf("comment authors are bot = %v, %v, want true, false", th.Comments[0].AuthorIsBot, th.Comments[1].AuthorIsBot)
	}
	if th.Comments[0].CommitSHA != "abc1234" || th.Comments[1].CommitSHA != "" {
		t.Errorf("comment commits = %q, %q, want abc1234 and none", th.Comments[0].CommitSHA, th.Comments[1].CommitSHA)
	}
	if !slices.Equal(pr.ReviewBodies, []string{"review"}) || !slices.Equal(pr.IssueComments, []string{"c"}) {
		t.Errorf("ReviewBodies, IssueComments = %q, %q", pr.ReviewBodies, pr.IssueComments)
	}
}

func TestParsePR_Errors(t *testing.T) {
	t.Parallel()
	over := strings.Replace(prJSON, `"reviewThreads":{"totalCount":1`, `"reviewThreads":{"totalCount":101`, 1)
	cases := []struct{ raw, want string }{
		{`not json`, "decode PR 7"},
		{`{"errors":[{"message":"boom"}]}`, "boom"},
		{`{"data":{"repository":{"pullRequest":null}}}`, "not found"},
		{over, "more than the 100"},
	}
	for _, tc := range cases {
		if _, err := parsePR([]byte(tc.raw), 7); !errContains(err, tc.want) {
			t.Errorf("parsePR error = %v, want one containing %q", err, tc.want)
		}
	}
}

func TestGitHub(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRunner{answers: map[string]string{
		"gh api graphql": prJSON,
		"gh pr list":     `[{"number":5},{"number":3}]`,
	}}
	gh := GitHub{Owner: "o", Name: "n", R: r}
	pr, err := gh.FetchPR(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 {
		t.Errorf("Number = %d, want 7", pr.Number)
	}
	if !strings.Contains(r.calls[0], "owner=o") {
		t.Errorf("the query should name the owner: %s", r.calls[0])
	}
	nums, err := gh.FetchRecentPRNumbers(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(nums, []int{5, 3}) {
		t.Errorf("FetchRecentPRNumbers = %v, want [5 3]", nums)
	}
	if !strings.Contains(r.calls[1], "--search sort:updated-desc") {
		t.Errorf("want the most recently updated PRs, not the most recently created: %s", r.calls[1])
	}

	boom := errors.New("boom")
	bad := GitHub{R: &fakeRunner{errs: map[string]error{"gh": boom}}}
	if _, err := bad.FetchPR(ctx, 1); !errors.Is(err, boom) {
		t.Errorf("FetchPR error = %v, want %v", err, boom)
	}
	if _, err := bad.FetchRecentPRNumbers(ctx, 1); !errors.Is(err, boom) {
		t.Errorf("FetchRecentPRNumbers error = %v, want %v", err, boom)
	}
	garbled := GitHub{R: &fakeRunner{answers: map[string]string{"gh": "x"}}}
	if _, err := garbled.FetchRecentPRNumbers(ctx, 1); !errContains(err, "decode pr list") {
		t.Errorf("a garbled list should fail to decode, got %v", err)
	}
}

func TestGitDiffer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRunner{answers: map[string]string{
		"git diff --no-renames --name-status": "M\ta.go\n",
		"git diff --no-renames -U0":           "@@ -10,2 +10,3 @@\n+x\n",
	}}
	g := NewGitDiffer(ctx, r)
	hunks, deleted, err := g.Hunks("c2", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if deleted || len(hunks) == 0 {
		t.Errorf("Hunks = %v, deleted %v, want hunks for a modified file", hunks, deleted)
	}
	if _, _, err := g.Hunks("c2", "a.go"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Errorf("git ran %d times, want 2: the second lookup comes from the cache", len(r.calls))
	}

	del := NewGitDiffer(ctx, &fakeRunner{answers: map[string]string{"git diff --no-renames --name-status": "D\ta.go\n"}})
	hunks, deleted, err = del.Hunks("c2", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if !deleted || len(hunks) != 0 {
		t.Errorf("Hunks = %v, deleted %v, want a deleted file and no hunks", hunks, deleted)
	}

	boom := errors.New("bad object")
	if _, _, err := NewGitDiffer(ctx, &fakeRunner{errs: map[string]error{"git": boom}}).Hunks("c2", "a.go"); !errors.Is(err, boom) {
		t.Errorf("a commit git cannot diff is an error, not an empty diff: got %v", err)
	}
	failsDiff := &fakeRunner{
		errs:    map[string]error{"git diff --no-renames -U0": boom},
		answers: map[string]string{"git diff --no-renames --name-status": "M"},
	}
	if _, _, err := NewGitDiffer(ctx, failsDiff).Hunks("c2", "a.go"); !errors.Is(err, boom) {
		t.Errorf("Hunks error = %v, want %v", err, boom)
	}
}

func TestFetchPRCommits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRunner{}
	if err := FetchPRCommits(ctx, r, "o/n", "12"); err != nil {
		t.Fatal(err)
	}
	if want := "git fetch --quiet --no-tags https://github.com/o/n.git pull/12/head"; r.calls[1] != want {
		t.Errorf("with no origin, fetch = %q, want %q", r.calls[1], want)
	}

	// When origin is --repo, its own credentials are used.
	for _, url := range []string{"git@github.com:O/N.git", "https://github.com/o/n.git", "ssh://git@github.com/o/n/"} {
		same := &fakeRunner{answers: map[string]string{"git remote get-url origin": url + "\n"}}
		if err := FetchPRCommits(ctx, same, "o/n", "12"); err != nil {
			t.Fatal(err)
		}
		if want := "git fetch --quiet --no-tags origin pull/12/head"; same.calls[1] != want {
			t.Errorf("origin %s: fetch = %q, want %q", url, same.calls[1], want)
		}
	}
	other := &fakeRunner{answers: map[string]string{"git remote get-url origin": "git@github.com:o/nn.git"}}
	if err := FetchPRCommits(ctx, other, "o/n", "12"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(other.calls[1], "https://github.com/o/n.git") {
		t.Errorf("origin is another repository, so fetch from --repo: %s", other.calls[1])
	}
}

func writeArtifact(t *testing.T, dir, name, findings, reviews string) {
	t.Helper()
	s := filepath.Join(dir, name, "sessions", "pr-1")
	if err := os.MkdirAll(s, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s, "findings.json"), []byte(findings), 0o600); err != nil {
		t.Fatal(err)
	}
	if reviews != "" {
		if err := os.WriteFile(filepath.Join(dir, name, "reviews.jsonl"), []byte(reviews), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := `{"scope":["a.go"],"findings":[{"id":"f1","fingerprint":"fp","file":"a.go","line":3,"severity":"warning","source":"llm","verdict":"confirmed","new":true,"question":{"kind":"caller","subject":" Foo "}},` +
		`{"id":"f2","fingerprint":"old","file":"a.go","line":9,"new":false},` +
		`{"id":"f3","fingerprint":"obj","file":"a.go","line":4,"verdict":{"status":"refuted"}},` +
		`{"id":"f4","fingerprint":"odd","file":"a.go","line":5,"verdict":7}]}`
	writeArtifact(t, dir, "redline-pr-4-200", f, `{"at":"2026-11-01T10:00:00Z"}`+"\n"+`{"at":"2026-11-01T11:00:00Z"}`+"\n"+"garbage\n")
	writeArtifact(t, dir, "redline-pr-4-100", f, `{"at":"2026-10-31T10:00:00Z"}`+"\n")
	writeArtifact(t, dir, "redline-pr-5-300", f, "")
	for _, d := range []string{"redline-pr-6-1", "unrelated"} { // the first has no findings.json
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "redline-pr-9-9"), nil, 0o600); err != nil { // a file, not a dir
		t.Fatal(err)
	}

	runs, skipped, err := LoadRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none", skipped)
	}
	if len(runs[4]) != 2 {
		t.Fatalf("PR 4 has %d runs, want 2", len(runs[4]))
	}
	if runs[4][0].RunID != 100 {
		t.Errorf("first run = %d, want 100: oldest first", runs[4][0].RunID)
	}
	last := runs[4][1]
	if len(last.Findings) != 3 {
		t.Fatalf("got %d findings, want 3: a finding on code the PR did not change is not the PR's finding", len(last.Findings))
	}
	verdicts := []string{last.Findings[0].Verdict, last.Findings[1].Verdict, last.Findings[2].Verdict}
	if want := []string{"confirmed", "refuted", ""}; !slices.Equal(verdicts, want) {
		t.Errorf("verdicts = %q, want %q", verdicts, want)
	}
	if want := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC); !last.ReviewedAt.Equal(want) {
		t.Errorf("ReviewedAt = %v, want the last model call %v", last.ReviewedAt, want)
	}
	if want := (findings.Question{Kind: findings.QuestionCaller, Subject: " Foo "}).Key(); last.Findings[0].QuestionKey != want {
		t.Errorf("QuestionKey = %q, want %q", last.Findings[0].QuestionKey, want)
	}
	if !slices.Equal(last.Scope, []string{"a.go"}) {
		t.Errorf("Scope = %q, want [a.go]", last.Scope)
	}
	if len(runs[5]) != 1 {
		t.Fatalf("PR 5 has %d runs, want 1", len(runs[5]))
	}
	if runs[5][0].ReviewedAt.IsZero() {
		t.Error("without reviews.jsonl, ReviewedAt falls back to the file's mtime")
	}
	if len(runs[6]) != 0 || len(runs[9]) != 0 {
		t.Errorf("a directory without findings.json and a plain file are not runs: %v, %v", runs[6], runs[9])
	}

	if _, _, err := LoadRuns(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory should fail")
	}

	bad := t.TempDir()
	writeArtifact(t, bad, "redline-pr-1-1", "{", "")
	writeArtifact(t, bad, "redline-pr-2-1", f, "")
	runs, skipped, err = LoadRuns(bad)
	if err != nil {
		t.Fatalf("one bad artifact should not hide the others: %v", err)
	}
	if len(skipped) != 1 || !errContains(skipped[0], "decode findings.json") {
		t.Errorf("skipped = %v, want the one that does not decode", skipped)
	}
	if len(runs[2]) != 1 {
		t.Errorf("PR 2 has %d runs, want 1", len(runs[2]))
	}
}

func TestWriteJSONL(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	if err := WriteJSONL(&b, []map[string]int{{"a": 1}, {"a": 2}}); err != nil {
		t.Fatal(err)
	}
	if want := "{\"a\":1}\n{\"a\":2}\n"; b.String() != want {
		t.Errorf("WriteJSONL wrote %q, want %q", b.String(), want)
	}
}

func TestRecorder_Record(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRunner{answers: map[string]string{
		"gh api graphql":                      prJSON,
		"git diff --no-renames --name-status": "M\ta.go\n",
		"git diff --no-renames -U0":           "@@ -9,1 +9,1 @@\n+x\n",
	}}
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	rec := Recorder{GH: GitHub{Owner: "o", Name: "n", R: r}, Git: r, Now: func() time.Time { return now }}
	res, err := rec.Record(ctx, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.PR.AuthorKind != "bot" {
		t.Errorf("AuthorKind = %q, want bot", res.PR.AuthorKind)
	}
	if want := "git fetch --quiet --no-tags https://github.com/o/n.git pull/7/head"; !slices.Contains(r.calls, want) {
		t.Errorf("the PR's commits should be fetched with %q; calls: %q", want, r.calls)
	}

	boom := errors.New("fetch failed")
	noFetch := Recorder{
		GH:  GitHub{R: &fakeRunner{answers: map[string]string{"gh": prJSON}}},
		Git: &fakeRunner{errs: map[string]error{"git fetch": boom}},
	}
	if _, err := noFetch.Record(ctx, 7, nil); !errors.Is(err, boom) {
		t.Errorf("a failed fetch: got %v, want %v", err, boom)
	}
	noGH := Recorder{GH: GitHub{R: &fakeRunner{errs: map[string]error{"gh": boom}}}}
	if _, err := noGH.Record(ctx, 7, nil); !errors.Is(err, boom) {
		t.Errorf("a failed gh: got %v, want %v", err, boom)
	}
}
