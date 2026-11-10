package dispositions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	require.NoError(t, err)
	assert.Equal(t, "MERGED", pr.State)
	assert.True(t, pr.AuthorIsBot)
	assert.Equal(t, time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC), pr.ClosedAt, "merge time wins over close time")
	require.Len(t, pr.Commits, 2)
	assert.Equal(t, "abc1234", pr.Commits[0].SHA, "shas are lowered")
	assert.True(t, pr.Commits[1].IsMerge)
	require.Len(t, pr.Threads, 1)
	assert.Equal(t, 9, pr.Threads[0].OrigLine)
	assert.True(t, pr.Threads[0].Comments[0].AuthorIsBot)
	assert.False(t, pr.Threads[0].Comments[1].AuthorIsBot)
	assert.Equal(t, "abc1234", pr.Threads[0].Comments[0].CommitSHA)
	assert.Empty(t, pr.Threads[0].Comments[1].CommitSHA)
	assert.Equal(t, []string{"review"}, pr.ReviewBodies)
	assert.Equal(t, []string{"c"}, pr.IssueComments)
}

func TestParsePR_Errors(t *testing.T) {
	t.Parallel()
	_, err := parsePR([]byte(`not json`), 7)
	require.ErrorContains(t, err, "decode PR 7")
	_, err = parsePR([]byte(`{"errors":[{"message":"boom"}]}`), 7)
	require.ErrorContains(t, err, "boom")
	_, err = parsePR([]byte(`{"data":{"repository":{"pullRequest":null}}}`), 7)
	require.ErrorContains(t, err, "not found")
	over := strings.Replace(prJSON, `"reviewThreads":{"totalCount":1`, `"reviewThreads":{"totalCount":101`, 1)
	_, err = parsePR([]byte(over), 7)
	require.ErrorContains(t, err, "more than the 100")
}

func TestGitHub(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{answers: map[string]string{
		"gh api graphql": prJSON,
		"gh pr list":     `[{"number":5},{"number":3}]`,
	}}
	gh := GitHub{Owner: "o", Name: "n", R: r}
	pr, err := gh.FetchPR(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, 7, pr.Number)
	assert.Contains(t, r.calls[0], "owner=o")
	nums, err := gh.FetchRecentPRNumbers(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, []int{5, 3}, nums)
	assert.Contains(t, r.calls[1], "--search sort:updated-desc", "most recently updated, not most recently created")

	boom := errors.New("boom")
	bad := GitHub{R: &fakeRunner{errs: map[string]error{"gh": boom}}}
	_, err = bad.FetchPR(context.Background(), 1)
	require.ErrorIs(t, err, boom)
	_, err = bad.FetchRecentPRNumbers(context.Background(), 1)
	require.ErrorIs(t, err, boom)
	_, err = GitHub{R: &fakeRunner{answers: map[string]string{"gh": "x"}}}.FetchRecentPRNumbers(context.Background(), 1)
	require.ErrorContains(t, err, "decode pr list")
}

func TestGitDiffer(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{answers: map[string]string{
		"git diff --no-renames --name-status": "M\ta.go\n",
		"git diff --no-renames -U0":           "@@ -10,2 +10,3 @@\n+x\n",
	}}
	g := NewGitDiffer(context.Background(), r)
	hunks, deleted, err := g.Hunks("c2", "a.go")
	require.NoError(t, err)
	assert.False(t, deleted)
	assert.NotEmpty(t, hunks)
	_, _, err = g.Hunks("c2", "a.go")
	require.NoError(t, err)
	assert.Len(t, r.calls, 2, "the second lookup comes from the cache")

	del := NewGitDiffer(context.Background(), &fakeRunner{answers: map[string]string{"git diff --no-renames --name-status": "D\ta.go\n"}})
	hunks, deleted, err = del.Hunks("c2", "a.go")
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Empty(t, hunks)

	boom := errors.New("bad object")
	_, _, err = (NewGitDiffer(context.Background(), &fakeRunner{errs: map[string]error{"git": boom}})).Hunks("c2", "a.go")
	require.ErrorIs(t, err, boom, "a commit git cannot diff is an error, not an empty diff")
	_, _, err = (NewGitDiffer(context.Background(), &fakeRunner{errs: map[string]error{"git diff --no-renames -U0": boom}, answers: map[string]string{"git diff --no-renames --name-status": "M"}})).Hunks("c2", "a.go")
	require.ErrorIs(t, err, boom)
}

func TestFetchPRCommits(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	require.NoError(t, FetchPRCommits(context.Background(), r, "o/n", "12"))
	assert.Equal(t, "git fetch --quiet --no-tags https://github.com/o/n.git pull/12/head", r.calls[1], "origin is not --repo")

	for _, url := range []string{"git@github.com:O/N.git", "https://github.com/o/n.git", "ssh://git@github.com/o/n/"} {
		same := &fakeRunner{answers: map[string]string{"git remote get-url origin": url + "\n"}}
		require.NoError(t, FetchPRCommits(context.Background(), same, "o/n", "12"))
		assert.Equal(t, "git fetch --quiet --no-tags origin pull/12/head", same.calls[1], "origin is --repo, so its own credentials are used: %s", url)
	}
	other := &fakeRunner{answers: map[string]string{"git remote get-url origin": "git@github.com:o/nn.git"}}
	require.NoError(t, FetchPRCommits(context.Background(), other, "o/n", "12"))
	assert.Contains(t, other.calls[1], "https://github.com/o/n.git")
}

func writeArtifact(t *testing.T, dir, name, findings, reviews string) {
	t.Helper()
	s := filepath.Join(dir, name, "sessions", "pr-1")
	require.NoError(t, os.MkdirAll(s, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(s, "findings.json"), []byte(findings), 0o600))
	if reviews != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name, "reviews.jsonl"), []byte(reviews), 0o600))
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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "redline-pr-6-1"), 0o755)) // no findings.json
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "unrelated"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "redline-pr-9-9"), nil, 0o600)) // a file, not a dir

	runs, skipped, err := LoadRuns(dir)
	require.NoError(t, err)
	assert.Empty(t, skipped)
	require.Len(t, runs[4], 2)
	got := runs[4][1].Findings
	require.Len(t, got, 3, "a finding on code the PR did not change is not the PR's finding")
	assert.Equal(t, []string{"confirmed", "refuted", ""}, []string{got[0].Verdict, got[1].Verdict, got[2].Verdict})
	assert.Equal(t, int64(100), runs[4][0].RunID, "oldest first")
	assert.Equal(t, time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC), runs[4][1].ReviewedAt, "the last model call")
	assert.Equal(t, findings.Question{Kind: findings.QuestionCaller, Subject: " Foo "}.Key(), runs[4][1].Findings[0].QuestionKey)
	assert.Equal(t, []string{"a.go"}, runs[4][1].Scope)
	require.Len(t, runs[5], 1)
	assert.False(t, runs[5][0].ReviewedAt.IsZero(), "falls back to the file's mtime")
	assert.Empty(t, runs[6])
	assert.Empty(t, runs[9])

	_, _, err = LoadRuns(filepath.Join(dir, "missing"))
	require.Error(t, err)

	bad := t.TempDir()
	writeArtifact(t, bad, "redline-pr-1-1", "{", "")
	writeArtifact(t, bad, "redline-pr-2-1", f, "")
	runs, skipped, err = LoadRuns(bad)
	require.NoError(t, err, "one bad artifact does not hide the others")
	require.Len(t, skipped, 1)
	require.ErrorContains(t, skipped[0], "decode findings.json")
	assert.Len(t, runs[2], 1)
}

func TestWriteJSONL(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	require.NoError(t, WriteJSONL(&b, []map[string]int{{"a": 1}, {"a": 2}}))
	assert.Equal(t, "{\"a\":1}\n{\"a\":2}\n", b.String())
}

func TestRecorder_Record(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{answers: map[string]string{
		"gh api graphql":                      prJSON,
		"git diff --no-renames --name-status": "M\ta.go\n",
		"git diff --no-renames -U0":           "@@ -9,1 +9,1 @@\n+x\n",
	}}
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	rec := Recorder{GH: GitHub{Owner: "o", Name: "n", R: r}, Git: r, Now: func() time.Time { return now }}
	res, err := rec.Record(context.Background(), 7, nil)
	require.NoError(t, err)
	assert.Equal(t, "bot", res.PR.AuthorKind)
	assert.Contains(t, strings.Join(r.calls, "\n"), "git fetch --quiet --no-tags https://github.com/o/n.git pull/7/head")

	boom := errors.New("fetch failed")
	_, err = (Recorder{GH: GitHub{R: &fakeRunner{answers: map[string]string{"gh": prJSON}, errs: map[string]error{"git fetch": boom}}}, Git: &fakeRunner{errs: map[string]error{"git fetch": boom}}}).Record(context.Background(), 7, nil)
	require.ErrorIs(t, err, boom)
	_, err = (Recorder{GH: GitHub{R: &fakeRunner{errs: map[string]error{"gh": boom}}}}).Record(context.Background(), 7, nil)
	require.ErrorIs(t, err, boom)
}
