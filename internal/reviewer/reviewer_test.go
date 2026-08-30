package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// reviewers.json is written by hand, so a timeout has to be readable there.
func TestTimeoutReadsAsADurationString(t *testing.T) {
	var a Adapter
	if err := json.Unmarshal([]byte(`{"name":"x","timeout":"90s"}`), &a); err != nil {
		t.Fatal(err)
	}
	if time.Duration(a.Timeout) != 90*time.Second {
		t.Fatalf("timeout = %v, want 90s", time.Duration(a.Timeout))
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"timeout":"1m30s"`) {
		t.Fatalf("timeout should round-trip as a string: %s", b)
	}
	if err := json.Unmarshal([]byte(`{"timeout":"drei minuten"}`), &a); err == nil {
		t.Fatal("an unparseable duration should be an error")
	}
}

// The likeliest failure is a reviewer that is not installed, and exec's error
// is the only thing that says so.
func TestRunNamesTheExecFailure(t *testing.T) {
	dir := t.TempDir()
	a := Adapter{Name: "ghost", Command: []string{"redline-no-such-reviewer"}, Timeout: Duration(5 * time.Second)}
	_, err := Run(context.Background(), a, dir, "HEAD", dir)
	if err == nil {
		t.Fatal("a missing reviewer binary must be an error")
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("error should name the exec failure, got: %v", err)
	}
}

// The reviewer runs in the tree under review, so a relative out directory must
// not resolve against its working directory.
func TestRunWritesToAnAbsoluteOutDir(t *testing.T) {
	outDir := t.TempDir()
	rel, err := filepath.Rel(mustGetwd(t), outDir)
	if err != nil {
		t.Skip("out dir is not addressable relative to the working directory")
	}
	elsewhere := t.TempDir()
	a := Adapter{
		Name:    "fake",
		Command: []string{"sh", "-c", `printf '{"findings":[{"file":"a.go","line":1,"severity":"error","title":"t","body":"b","confidence":"high"}]}' > "$0"`, "{out}"},
		Timeout: Duration(5 * time.Second),
	}
	got, err := Run(context.Background(), a, elsewhere, "HEAD", rel)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}
	if _, err := os.Stat(filepath.Join(outDir, "reviewer-fake.json")); err != nil {
		t.Fatalf("findings should land in the out dir, not the review tree: %v", err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestBuiltins(t *testing.T) {
	builtins := Builtins()
	if _, ok := builtins["claude"]; !ok {
		t.Fatal("claude adapter missing")
	}
	if _, ok := builtins["cursor"]; !ok {
		t.Fatal("cursor adapter missing")
	}
	if len(builtins) != 2 {
		t.Fatalf("expected 2 builtins, got %d", len(builtins))
	}
}

func TestLoadNoFile(t *testing.T) {
	dir := t.TempDir()
	adapters, err := Load(dir)
	if err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if len(adapters) != 2 {
		t.Fatalf("expected 2 builtin adapters, got %d", len(adapters))
	}
	if _, ok := adapters["claude"]; !ok {
		t.Fatal("claude adapter missing")
	}
}

func TestLoadWithValidFile(t *testing.T) {
	dir := t.TempDir()
	reviewersJSON := filepath.Join(dir, "reviewers.json")
	file := struct {
		Reviewers map[string]Adapter `json:"reviewers"`
	}{
		Reviewers: map[string]Adapter{
			"claude": {
				Name:    "claude",
				Command: []string{"custom-claude", "-p", "{prompt}"},
				Timeout: 5 * 60,
			},
			"custom": {
				Name:    "custom",
				Command: []string{"my-reviewer"},
				Timeout: 3 * 60,
			},
		},
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(reviewersJSON, data, 0o644); err != nil {
		t.Fatal(err)
	}

	adapters, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// File entries override built-ins.
	if adapters["claude"].Command[0] != "custom-claude" {
		t.Fatalf("claude not overridden: %v", adapters["claude"].Command)
	}

	// New entries from file are added.
	if _, ok := adapters["custom"]; !ok {
		t.Fatal("custom adapter missing")
	}

	// cursor stays from builtins.
	if _, ok := adapters["cursor"]; !ok {
		t.Fatal("cursor adapter missing from builtins")
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	reviewersJSON := filepath.Join(dir, "reviewers.json")
	if err := os.WriteFile(reviewersJSON, []byte("{invalid json}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load should error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "reviewers.json") {
		t.Fatalf("error should name the file: %v", err)
	}
}

// The claude adapter must invoke Claude Code's own /code-review rather than a
// prompt Redline wrote: the vendor's review logic is the thing being borrowed.
func TestClaudePromptInvokesTheToolsOwnReview(t *testing.T) {
	got := Builtins()["claude"].prompt("HEAD", "/o.json")
	if !strings.HasPrefix(got, "/code-review HEAD") {
		t.Fatalf("claude prompt should lead with its own review command, got: %s", got)
	}
	if !strings.Contains(got, "/o.json") {
		t.Fatalf("prompt missing the output path: %s", got)
	}
}

// An adapter whose command is already a review command needs no prompt of its
// own; it still has to be told where to write.
func TestPromptFallsBackToTheContractAlone(t *testing.T) {
	prompt := Adapter{}.prompt("/path/to/target", "/path/to/out.json")

	if !strings.Contains(prompt, "/path/to/out.json") {
		t.Fatalf("prompt missing outPath: %s", prompt)
	}
	if !strings.Contains(prompt, "findings") {
		t.Fatalf("prompt missing 'findings': %s", prompt)
	}
	if !strings.Contains(prompt, "severity") {
		t.Fatalf("prompt should carry the schema: %s", prompt)
	}
}

func TestPlaceholderSubstitution(t *testing.T) {
	a := Adapter{
		Name:    "test",
		Command: []string{"cmd", "{prompt}", "{out}", "{target}", "{schema}"},
		Prompt:  "review {target}",
		Timeout: Duration(1 * time.Second),
	}

	cmd := a.argv("/my/target", "/my/out.json")

	if cmd[0] != "cmd" {
		t.Fatalf("cmd[0] = %q, want cmd", cmd[0])
	}
	if !strings.HasPrefix(cmd[1], "review /my/target") {
		t.Fatalf("the adapter's own prompt should be expanded into {prompt}: %s", cmd[1])
	}
	if cmd[2] != "/my/out.json" || cmd[3] != "/my/target" {
		t.Fatalf("out/target placeholders wrong: %q %q", cmd[2], cmd[3])
	}
	if !strings.Contains(cmd[4], "severity") {
		t.Fatalf("schema placeholder not expanded: %s", cmd[4])
	}
}

func TestRunWithFakeAdapter(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a fixture findings file that a fake "reviewer" would write.
	fixtureFindings := map[string]interface{}{
		"findings": []map[string]interface{}{
			{
				"file":       "main.go",
				"line":       42,
				"severity":   "error",
				"title":      "Error in main",
				"body":       "Something is wrong",
				"confidence": "high",
			},
			{
				"file":       "util.go",
				"line":       10,
				"severity":   "warning",
				"title":      "Warning in util",
				"body":       "Could be better",
				"confidence": "medium",
			},
		},
	}

	fixtureData, err := json.Marshal(fixtureFindings)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}

	// Use a fake adapter that writes the fixture file.
	// We use 'sh -c' to write the file without fork-bombing ourselves.
	fixtureFile := filepath.Join(tmpDir, "fixture.json")
	if err := os.WriteFile(fixtureFile, fixtureData, 0o644); err != nil {
		t.Fatal(err)
	}

	a := Adapter{
		Name:    "fake",
		Command: []string{"sh", "-c", fmt.Sprintf("cp %q {out}", fixtureFile)},
		Timeout: Duration(5 * time.Second),
	}

	findings, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	if findings[0].File != "main.go" || findings[0].Line != 42 {
		t.Fatalf("finding[0] location: %s:%d", findings[0].File, findings[0].Line)
	}
	if findings[0].Severity != "error" {
		t.Fatalf("finding[0] severity: %q", findings[0].Severity)
	}
	if findings[0].Title != "Error in main" {
		t.Fatalf("finding[0] title: %q", findings[0].Title)
	}
}

func TestRunStaleFileRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-populate an old findings file.
	oldOut := filepath.Join(outDir, "reviewer-stale.json")
	oldFindings := map[string]interface{}{
		"findings": []map[string]interface{}{
			{
				"file":       "old.go",
				"line":       1,
				"severity":   "error",
				"title":      "Old finding",
				"body":       "This should be removed",
				"confidence": "high",
			},
		},
	}
	oldData, err := json.Marshal(oldFindings)
	if err != nil {
		t.Fatalf("marshal old: %v", err)
	}
	if err := os.WriteFile(oldOut, oldData, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a fake adapter that writes a new findings file.
	fixtureFindings := map[string]interface{}{
		"findings": []map[string]interface{}{
			{
				"file":       "new.go",
				"line":       2,
				"severity":   "warning",
				"title":      "New finding",
				"body":       "This is fresh",
				"confidence": "medium",
			},
		},
	}
	fixtureData, err := json.Marshal(fixtureFindings)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	fixtureFile := filepath.Join(tmpDir, "fixture.json")
	if err := os.WriteFile(fixtureFile, fixtureData, 0o644); err != nil {
		t.Fatal(err)
	}

	a := Adapter{
		Name:    "stale",
		Command: []string{"sh", "-c", fmt.Sprintf("cp %q {out}", fixtureFile)},
		Timeout: Duration(5 * time.Second),
	}

	findings, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].File != "new.go" {
		t.Fatalf("old finding still present: %s", findings[0].File)
	}
}

func TestRunMissingOutFile(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a fake adapter that does nothing (doesn't write the output file).
	a := Adapter{
		Name:    "failing",
		Command: []string{"sh", "-c", "true"},
		Timeout: Duration(5 * time.Second),
	}

	_, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	if err == nil {
		t.Fatal("Run should error when output file is missing")
	}
	if !strings.Contains(err.Error(), "no findings file") {
		t.Fatalf("error should mention missing file: %v", err)
	}
	if !strings.Contains(err.Error(), "failing") {
		t.Fatalf("error should name the reviewer: %v", err)
	}
}

func TestRunNormalizeSeverityAndConfidence(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create findings with mixed case and invalid values.
	fixtureFindings := map[string]interface{}{
		"findings": []map[string]interface{}{
			{
				"file":       "test.go",
				"line":       1,
				"severity":   "ERROR",
				"title":      "Upper case severity",
				"body":       "Test",
				"confidence": "HIGH",
			},
			{
				"file":       "test.go",
				"line":       2,
				"severity":   "InvalidSev",
				"title":      "Invalid severity",
				"body":       "Test",
				"confidence": "InvalidConf",
			},
		},
	}
	fixtureData, err := json.Marshal(fixtureFindings)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	fixtureFile := filepath.Join(tmpDir, "fixture.json")
	if err := os.WriteFile(fixtureFile, fixtureData, 0o644); err != nil {
		t.Fatal(err)
	}

	a := Adapter{
		Name:    "normalize",
		Command: []string{"sh", "-c", fmt.Sprintf("cp %q {out}", fixtureFile)},
		Timeout: Duration(5 * time.Second),
	}

	findings, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	if findings[0].Severity != "error" {
		t.Fatalf("finding[0] severity not normalized: %q", findings[0].Severity)
	}
	if findings[0].Confidence != "high" {
		t.Fatalf("finding[0] confidence not normalized: %q", findings[0].Confidence)
	}

	if findings[1].Severity != "warning" {
		t.Fatalf("finding[1] invalid severity should default to warning: %q", findings[1].Severity)
	}
	if findings[1].Confidence != "medium" {
		t.Fatalf("finding[1] invalid confidence should default to medium: %q", findings[1].Confidence)
	}
}

func TestRunDropsEmptyTitles(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create findings where some have empty titles.
	fixtureFindings := map[string]interface{}{
		"findings": []map[string]interface{}{
			{
				"file":       "test.go",
				"line":       1,
				"severity":   "error",
				"title":      "Valid title",
				"body":       "Test",
				"confidence": "high",
			},
			{
				"file":       "test.go",
				"line":       2,
				"severity":   "warning",
				"title":      "",
				"body":       "This should be dropped",
				"confidence": "medium",
			},
		},
	}
	fixtureData, err := json.Marshal(fixtureFindings)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	fixtureFile := filepath.Join(tmpDir, "fixture.json")
	if err := os.WriteFile(fixtureFile, fixtureData, 0o644); err != nil {
		t.Fatal(err)
	}

	a := Adapter{
		Name:    "dropempty",
		Command: []string{"sh", "-c", fmt.Sprintf("cp %q {out}", fixtureFile)},
		Timeout: Duration(5 * time.Second),
	}

	findings, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding after dropping empty titles, got %d", len(findings))
	}
	if findings[0].Title != "Valid title" {
		t.Fatalf("wrong finding returned: %q", findings[0].Title)
	}
}

// The complaint this answers: --with printed nothing, so a running reviewer and
// a hung one looked identical.
func TestRunReportsProgressWhileTheReviewerWorks(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Writes a line, waits, writes another, then produces findings.
	a := Adapter{
		Name: "chatty",
		Command: []string{"sh", "-c",
			`echo "scanning files"; sleep 0.25; echo "writing findings"; sleep 0.25; ` +
				`printf '{"findings":[{"title":"x","severity":"warning"}]}' > "$0"`,
			filepath.Join(outDir, "reviewer-chatty.json")},
		Timeout: Duration(10 * time.Second),
	}

	var mu sync.Mutex
	var updates []Update
	_, err := Run(context.Background(), a, tmpDir, tmpDir, outDir,
		WithProgress(50*time.Millisecond, func(u Update) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
		}))
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) < 3 {
		t.Fatalf("expected a start and several ticks, got %d: %+v", len(updates), updates)
	}
	// The first update is the start, so something appears immediately rather
	// than one interval later.
	if updates[0].Elapsed != 0 {
		t.Errorf("first update should be the start, got elapsed %v", updates[0].Elapsed)
	}
	if updates[0].Name != "chatty" {
		t.Errorf("update should name the reviewer, got %q", updates[0].Name)
	}

	last := updates[len(updates)-1]
	if last.Elapsed <= 0 {
		t.Error("ticks should carry elapsed time")
	}
	if last.Bytes == 0 {
		t.Error("ticks should carry how much the reviewer has written")
	}
	// The reviewer's own output is the most useful progress there is.
	if last.Last != "writing findings" {
		t.Errorf("last line = %q, want the reviewer's most recent output", last.Last)
	}
	// Elapsed must increase, or the caller cannot tell a live tick from a stuck one.
	for i := 2; i < len(updates); i++ {
		if updates[i].Elapsed < updates[i-1].Elapsed {
			t.Fatalf("elapsed went backwards: %v then %v", updates[i-1].Elapsed, updates[i].Elapsed)
		}
	}
}

// A reviewer that produced nothing at all is the case worth distinguishing, and
// the timeout message is where the reader looks for it.
func TestTimeoutSaysWhatTheReviewerManagedToDo(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	silent := Adapter{
		Name:    "silent",
		Command: []string{"sh", "-c", "sleep 10"},
		Timeout: Duration(150 * time.Millisecond),
	}
	_, err := Run(context.Background(), silent, tmpDir, tmpDir, outDir)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "wrote 0 byte") {
		t.Errorf("a silent reviewer should be reported as silent: %v", err)
	}

	talkative := Adapter{
		Name:    "talkative",
		Command: []string{"sh", "-c", "echo working on it; sleep 10"},
		Timeout: Duration(300 * time.Millisecond),
	}
	_, err = Run(context.Background(), talkative, tmpDir, tmpDir, outDir)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "working on it") {
		t.Errorf("the timeout should quote what it last said: %v", err)
	}
}

// A reviewer that leaves a child running must not hang Redline past its own
// timeout. Killing the reviewer does not kill what it spawned, and the output
// pipe stays open while any of them holds it — so without a wait delay the
// deadline does not bound anything, which is the failure it exists to prevent.
func TestTimeoutIsBoundedEvenWhenTheReviewerLeavesChildrenBehind(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	a := Adapter{
		Name: "forker",
		// The shell exits immediately; the background sleep inherits its
		// stdout and would hold the pipe open for a minute.
		Command: []string{"sh", "-c", "sleep 60 & exit 0"},
		Timeout: Duration(200 * time.Millisecond),
	}

	start := time.Now()
	_, err := Run(context.Background(), a, tmpDir, tmpDir, outDir)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error: the reviewer wrote no findings file")
	}
	// Generous bound: the point is seconds, not the full sixty.
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %v — the orphaned child held it open past the deadline", elapsed)
	}
}

func TestRunTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	outDir := filepath.Join(tmpDir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use a slow fake adapter that sleeps longer than the timeout.
	a := Adapter{
		Name:    "slow",
		Command: []string{"sh", "-c", "sleep 10"},
		Timeout: Duration(100 * time.Millisecond),
	}

	ctx := context.Background()
	_, err := Run(ctx, a, tmpDir, tmpDir, outDir)
	if err == nil {
		t.Fatal("Run should error on timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error should mention timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Fatalf("error should name the reviewer: %v", err)
	}
}
