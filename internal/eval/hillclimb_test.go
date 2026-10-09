package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

// hillclimbRecorder writes a sweep's samples in the layout the hillclimb
// report reads: one results row per fixture and sample, the samples that
// produced nothing beside them, a trace per sample, and each fixture's packet
// once per variant.
//
// A sweep's own table scores the union of a fixture's samples, which is what a
// reader of a sampled review is handed. A hillclimb needs the other number:
// every sample scored on its own, so a round's spread can be read from its own
// rows and a change can be judged against it. Nothing here scores differently
// from the sweep; it scores each review through the same Score.
//
// Off unless REDLINE_HILLCLIMB_DIR names a variant directory. Two sweeps can
// fill one variant in parallel: each writes its own shard of rows, named by
// REDLINE_HILLCLIMB_SHARD, and REDLINE_HILLCLIMB_REP_OFFSET keeps their rep
// numbers apart. The shards are concatenated once both have exited, because
// two processes appending to one file can interleave a line.
type hillclimbRecorder struct {
	dir     string
	variant string
	shard   string
	rep0    int
}

func newHillclimbRecorder(t *testing.T) *hillclimbRecorder {
	t.Helper()
	dir := os.Getenv("REDLINE_HILLCLIMB_DIR")
	if dir == "" {
		return nil
	}
	// go test runs a package's binary from that package's directory, so a
	// relative path ends up under internal/eval, outside the gitignored
	// .redline at the repository root. The first baseline was written there
	// before this check existed.
	if !filepath.IsAbs(dir) {
		t.Fatalf("REDLINE_HILLCLIMB_DIR=%q must be an absolute path: go test runs from the package directory, so a relative one lands under internal/eval", dir)
	}
	rep0 := 0
	if s := os.Getenv("REDLINE_HILLCLIMB_REP_OFFSET"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			t.Fatalf("REDLINE_HILLCLIMB_REP_OFFSET=%q is not a non-negative count", s)
		}
		rep0 = n
	}
	shard := os.Getenv("REDLINE_HILLCLIMB_SHARD")
	if shard == "" {
		shard = "0"
	}
	for _, sub := range []string{"traces", "prompts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &hillclimbRecorder{dir: dir, variant: filepath.Base(dir), shard: shard, rep0: rep0}
}

// servedAsRequested allows an alias that resolves to a dated snapshot of
// itself and nothing else. An empty served model means the wire did not say,
// which is recorded rather than refused.
func servedAsRequested(requested, served string) bool {
	return served == "" || served == requested || strings.HasPrefix(served, requested+"-")
}

func (h *hillclimbRecorder) recordSample(t *testing.T, f Fixture, si int, out *review.Result) {
	t.Helper()
	if h == nil {
		return
	}
	if !servedAsRequested(out.Model, out.ServedModel) {
		err := fmt.Errorf("requested %s and the response was served by %s", out.Model, out.ServedModel)
		t.Errorf("%s: %v", f.Annotation.Name, err)
		h.recordError(t, f, si, out, err)
		return
	}
	rep := h.rep0 + si
	grade := hillclimbGrade(f, out.Review, out.Stubs)
	tag := "defect"
	if f.Annotation.Clean {
		tag = "clean"
	}

	promptRef := h.writePrompt(t, f, out)
	row := map[string]any{
		"prompt_id":       f.Annotation.Name,
		"rep":             rep,
		"prompt":          f.Annotation.Summary,
		"tags":            []string{tag},
		"grade":           grade,
		"model":           out.ServedModel,
		"requested_model": out.Model,
		"usage":           usageRow(out.Usage),
		"stop_reason":     out.StopReason,
		"latency_s":       out.Duration.Seconds(),
		"attachments":     []map[string]string{{"kind": "text", "ref": promptRef, "alt": "the packet this sample reviewed"}},
	}
	if out.CostKnown {
		row["cost_usd"] = out.CostUSD
	}
	h.appendJSONL(t, "results", row)

	body, err := json.MarshalIndent(out.Review, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	trace := []map[string]string{
		{"role": "system", "content": out.System},
		{"role": "user", "content": "The packet this sample reviewed is " + promptRef + "."},
		{"role": "assistant", "content": string(body)},
	}
	h.writeJSON(t, filepath.Join("traces", fmt.Sprintf("%s_rep%d.json", f.Annotation.Name, rep)), trace)
}

// hillclimbGrade scores one review of one fixture for a results row. Shared by
// the recorder and the re-grader, so a row written during a sweep and a row
// re-graded afterwards are graded by the same code.
func hillclimbGrade(f Fixture, rev findings.Review, stubs int) map[string]float64 {
	sc := Score(f, rev)
	var expected, caught int
	caughtKeys := map[string]bool{}
	for _, k := range sc.Caught {
		caughtKeys[k] = true
	}
	for _, e := range f.Annotation.Expect {
		if e.Optional {
			continue
		}
		expected++
		if caughtKeys[e.Key] {
			caught++
		}
	}
	grade := map[string]float64{
		"unlabelled":      float64(sc.Extra),
		"false_positives": float64(len(sc.QuietViolations) + len(sc.Rejected)),
		"comments":        float64(len(rev.Comments)),
		"stubs":           float64(stubs),
	}
	if expected > 0 {
		grade["caught"] = float64(caught)
		grade["catch_rate"] = float64(caught) / float64(expected)
	}
	if f.Annotation.Clean {
		grade["clean_held"] = 0
		if len(rev.Comments) == 0 {
			grade["clean_held"] = 1
		}
	}
	return grade
}

// TestHillclimbRegrade re-grades a variant's rows from its stored traces,
// after the scorer or the labels change. The model is not called: every row's
// review is already on disk, and re-running it would put new samples under an
// old variant's name. The previous results are kept beside the new ones, so
// the old and new grades can be compared before anything is concluded from
// them.
//
//	REDLINE_HILLCLIMB_REGRADE=/abs/flow/v2 go test ./internal/eval -run TestHillclimbRegrade -v
func TestHillclimbRegrade(t *testing.T) {
	dir := os.Getenv("REDLINE_HILLCLIMB_REGRADE")
	if dir == "" {
		t.Skip("set REDLINE_HILLCLIMB_REGRADE to a variant directory")
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("REDLINE_HILLCLIMB_REGRADE=%q must be an absolute path", dir)
	}
	fixtures := load(t)
	byName := map[string]Fixture{}
	for _, f := range fixtures {
		byName[f.Annotation.Name] = f
	}
	path := filepath.Join(dir, "results.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	var changed int
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		name, _ := row["prompt_id"].(string)
		rep := int(row["rep"].(float64))
		f, ok := byName[name]
		if !ok {
			t.Fatalf("row %d names %q, which is not a fixture", i, name)
		}
		tr, err := os.ReadFile(filepath.Join(dir, "traces", fmt.Sprintf("%s_rep%d.json", name, rep)))
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		var turns []map[string]string
		if err := json.Unmarshal(tr, &turns); err != nil {
			t.Fatalf("%s rep %d: %v", name, rep, err)
		}
		var rev findings.Review
		if err := json.Unmarshal([]byte(turns[len(turns)-1]["content"]), &rev); err != nil {
			t.Fatalf("%s rep %d: %v", name, rep, err)
		}
		// Stubs were dropped before the review was stored, so the trace cannot
		// recount them; the row's own count stands.
		stubs := 0
		if old, ok := row["grade"].(map[string]any); ok {
			if s, ok := old["stubs"].(float64); ok {
				stubs = int(s)
			}
		}
		grade := hillclimbGrade(f, rev, stubs)
		before, _ := json.Marshal(row["grade"])
		after, _ := json.Marshal(grade)
		if string(before) != string(after) {
			changed++
		}
		row["grade"] = grade
		buf, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(buf))
	}
	backup := filepath.Join(dir, "results.before-regrade.jsonl")
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if err := os.WriteFile(backup, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: re-graded %d rows, %d changed", filepath.Base(dir), len(out), changed)
}

// recordError keeps a sample that produced no scorable review out of the
// results, where it would read as a review that found nothing.
func (h *hillclimbRecorder) recordError(t *testing.T, f Fixture, si int, out *review.Result, cause error) {
	t.Helper()
	if h == nil {
		return
	}
	class := "serving"
	switch {
	case strings.Contains(cause.Error(), "answered the contract without"):
		class = "stub"
	case strings.Contains(cause.Error(), "did not parse as a review"):
		// The model answered, and what it wrote broke the contract. Counted
		// apart from the endpoint failing, because a shape that raises this
		// rate is a worse reviewer rather than a worse day for the API.
		class = "malformed"
	case strings.Contains(cause.Error(), "response was served by"):
		class = "served-model-mismatch"
	case errors.Is(cause, os.ErrDeadlineExceeded):
		class = "timeout"
	}
	row := map[string]any{
		"prompt_id": f.Annotation.Name,
		"rep":       h.rep0 + si,
		"class":     class,
		"error":     cause.Error(),
	}
	if out != nil {
		row["model"] = out.ServedModel
		row["requested_model"] = out.Model
		row["usage"] = usageRow(out.Usage)
		row["stop_reason"] = out.StopReason
		if out.CostKnown {
			row["cost_usd"] = out.CostUSD
		}
	}
	h.appendJSONL(t, "errors", row)
}

func usageRow(u review.Usage) map[string]int64 {
	return map[string]int64{
		"input_tokens":                u.InputTokens,
		"output_tokens":               u.OutputTokens,
		"cache_read_input_tokens":     u.CacheReadTokens,
		"cache_creation_input_tokens": u.CacheWriteTokens,
	}
}

// writePrompt stores the fixture's packet once per variant and returns its
// path relative to the flow root, which is where the report resolves refs.
// The packet is the same for every sample of a fixture within a variant, and
// at up to a few hundred kilobytes it would dominate the rows if each carried
// it.
func (h *hillclimbRecorder) writePrompt(t *testing.T, f Fixture, out *review.Result) string {
	t.Helper()
	rel := filepath.Join("prompts", f.Annotation.Name+".txt")
	path := filepath.Join(h.dir, rel)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		packet := out.Prompt
		if out.Tail != "" {
			packet += "\n\n" + out.Tail
		}
		if err := os.WriteFile(path, []byte(packet), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.ToSlash(filepath.Join(h.variant, rel))
}

func (h *hillclimbRecorder) appendJSONL(t *testing.T, name string, row map[string]any) {
	t.Helper()
	line, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, fmt.Sprintf("%s.%s.jsonl", name, h.shard))
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (h *hillclimbRecorder) writeJSON(t *testing.T, rel string, v any) {
	t.Helper()
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, rel), append(buf, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The alias rule is the whole of the served-model check, so it is pinned: a
// dated snapshot of the requested model passes, and a different model that
// happens to share a prefix does not.
func TestServedModelMatchesOnlyItsOwnSnapshots(t *testing.T) {
	for _, tc := range []struct {
		requested, served string
		want              bool
	}{
		{"claude-sonnet-5", "claude-sonnet-5", true},
		{"claude-sonnet-5", "claude-sonnet-5-20260901", true},
		{"claude-sonnet-5", "", true},
		{"claude-sonnet-5", "claude-sonnet-50", false},
		{"claude-sonnet-5", "claude-opus-5", false},
		{"gpt-5", "gpt-5.6-terra", false},
	} {
		if got := servedAsRequested(tc.requested, tc.served); got != tc.want {
			t.Errorf("servedAsRequested(%q, %q) = %v, want %v", tc.requested, tc.served, got, tc.want)
		}
	}
}
