package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
)

func stagedInput() Input {
	in := exploreInput()
	for _, path := range []string{
		"internal/queue/retry.go", "internal/api/handler.go",
		"internal/store/schema.sql", "internal/store/migrate.go",
	} {
		in.Change.Files = append(in.Change.Files, change.File{
			Path: path, Diff: "@@ -1,2 +1,2 @@\n-old\n+new\n", Added: 1, Removed: 1,
		})
	}
	return in
}

func stagedOpts(api *exploreAPI) Options {
	return Options{
		API: APIAnthropic, BaseURL: api.srv.URL, APIKey: "k",
		Model: "claude-sonnet-5", MaxTokens: 4096, MaxCostUSD: 5,
		Cohorts: 6, CrossSummaries: true,
	}
}

const partitionBody = `{"overview":"Queue and store both move.","files":[` +
	`{"path":"internal/queue/q.go","summary":"drops a guard"},` +
	`{"path":"internal/queue/retry.go","summary":"retries once more"},` +
	`{"path":"internal/api/handler.go","summary":"new route"},` +
	`{"path":"internal/store/schema.sql","summary":"adds a column"},` +
	`{"path":"internal/store/migrate.go","summary":"runs the migration"}],"cohorts":[` +
	`{"name":"queue","summary":"The retry path.","files":["internal/queue/q.go","internal/queue/retry.go"]},` +
	`{"name":"storage","summary":"A column and the migration that adds it.",` +
	`"files":["internal/store/schema.sql","internal/store/migrate.go"]},` +
	`{"name":"api","summary":"One new route.","files":["internal/api/handler.go"]}]}`

func cohortFindings(file, body string) string {
	return `{"comments":[{"file":"` + file + `","line":1,"severity":"warning","confidence":"high",` +
		`"category":"review","relatedFindings":[],"body":"` + body + `",` +
		`"question":{"kind":"none","ask":"","subject":""}}],"verdicts":[]}`
}

// The shape of the run: one describing call draws the partition, one call per
// cohort judges it, and the reader is handed a single review carrying stage
// one's walkthrough and every cohort's findings.
func TestTheFanOutJudgesEachCohortAndMergesThem(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t0", StageCohorts, partitionBody)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageFindings,
			cohortFindings("internal/queue/q.go", "the retry loses the entry"))),
	)
	res, err := Run(context.Background(), stagedInput(), stagedOpts(api))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pipeline != PipelineStaged || res.FellBack != "" {
		t.Fatalf("the run did not stay staged: %q %q", res.Pipeline, res.FellBack)
	}
	if len(res.Cohorts) != 3 {
		t.Fatalf("the partition must reach the result: %+v", res.Cohorts)
	}
	// Four calls: stage one and one per cohort. The fake replays its last
	// script entry, so every cohort answers with the same finding, and the
	// union is what a reader would be handed - one remark, not three.
	if got := len(api.seen()); got != 4 {
		t.Errorf("a partition of three costs stage one plus three calls, got %d", got)
	}
	if len(res.Review.Comments) != 1 {
		t.Errorf("the cohorts' findings must be unioned, got %d: %+v",
			len(res.Review.Comments), res.Review.Comments)
	}
	if res.Review.Overview != "Queue and store both move." {
		t.Errorf("stage one's walkthrough must survive the fan-out: %q", res.Review.Overview)
	}
	if len(res.Review.Files) != 5 {
		t.Errorf("every shown file's line must survive: %v", res.Review.Files)
	}
}

// Each cohort call is scoped by its instruction and by nothing else: the
// prefix carries every diff because that is what the cache is keyed on, so
// what differs between two cohort calls is the tail and only the tail.
func TestEachCohortIsScopedByItsTailOverOneSharedPrefix(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t0", StageCohorts, partitionBody)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageFindings,
			cohortFindings("internal/queue/q.go", "x"))),
	)
	opts := stagedOpts(api)
	opts.Cache = true
	opts.CacheTTL = CacheTTL5m
	if _, err := Run(context.Background(), stagedInput(), opts); err != nil {
		t.Fatal(err)
	}
	var prefixes, tails []string
	for i, raw := range api.seen() {
		var req wireRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		blocks := req.Messages[0].Content
		if len(blocks) != 2 {
			t.Fatalf("request %d sends %d block(s), want the shared prefix and a tail", i, len(blocks))
		}
		if !hasCacheControl(blocks[0]) {
			t.Errorf("request %d does not mark the shared block", i)
		}
		prefixes = append(prefixes, blocks[0].Text)
		tails = append(tails, blocks[1].Text)
	}
	for i := 1; i < len(prefixes); i++ {
		if prefixes[i] != prefixes[0] {
			t.Fatalf("request %d sends a different prefix, so nothing after stage one reads the cache", i)
		}
	}
	// Every cohort's own files named, and no two cohorts given the same job.
	for i := 1; i < len(tails); i++ {
		for j := i + 1; j < len(tails); j++ {
			if tails[i] == tails[j] {
				t.Fatalf("cohort calls %d and %d were given the same instruction", i, j)
			}
		}
	}
	var storage string
	for _, tail := range tails[1:] {
		if strings.Contains(tail, "Your cohort: storage") {
			storage = tail
		}
	}
	if storage == "" {
		t.Fatal("no call was given the storage cohort")
	}
	if !strings.Contains(storage, "- internal/store/schema.sql\n") {
		t.Errorf("the storage call was not told which files are its own:\n%s", storage)
	}
	if !strings.Contains(storage, "The retry path.") {
		t.Errorf("cross-summaries are on, so the neighbours must be named:\n%s", storage)
	}
	if strings.Contains(storage, "- internal/queue/q.go\n") {
		t.Errorf("a neighbour's files were handed to the storage call as its own:\n%s", storage)
	}
}

// Stage one is the call everything else depends on. When it fails the run is
// a one-shot review that has already paid for a failed call, which is worse
// than a one-shot review and much better than no review - and the ledger has
// to be able to tell the two apart.
func TestAFailedStageOneFallsBackToOneCall(t *testing.T) {
	api := serveSSE(t,
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t0", StageCohorts, `{"overview":"","files":[],"cohorts":[]}`)),
		anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageReview, reviewBody)),
	)
	res, err := Run(context.Background(), stagedInput(), stagedOpts(api))
	if err != nil {
		t.Fatal(err)
	}
	if res.Pipeline != PipelineOneShot || res.FellBack == "" {
		t.Fatalf("a failed stage one must be recorded as a fallback: %q %q", res.Pipeline, res.FellBack)
	}
	if len(api.seen()) != 2 {
		t.Errorf("the fallback is one call after stage one, got %d", len(api.seen()))
	}
	if len(res.Review.Comments) != 1 || res.Review.Overview == "" {
		t.Fatalf("the fallback must produce a whole review: %+v", res.Review)
	}
}

// A partition is repaired rather than rejected: the alternative is throwing
// away a paid call over a file named twice.
func TestThePartitionIsRepairedRatherThanRefused(t *testing.T) {
	shown := map[string]bool{"a.go": true, "b.go": true, "c.go": true}
	got, note := repairPartition(shown, []Cohort{
		{Name: "one", Files: []string{"a.go", "b.go"}},
		{Name: "two", Files: []string{"b.go", "gone.go"}},
		{Name: "empty", Files: []string{"also-gone.go"}},
	}, 6)
	if len(got) != 1 || len(got[0].Files) != 3 {
		t.Fatalf("every shown file lands in exactly one cohort: %+v", got)
	}
	if got[0].Files[2] != "c.go" {
		t.Errorf("a file in no cohort must be placed, not dropped: %v", got[0].Files)
	}
	if note == "" {
		t.Error("a repaired partition must say what it changed")
	}
}

// The bound is what the tripwire priced. A stage one free to return nine
// cohorts would commit the run to nine calls nobody agreed to pay for.
func TestThePartitionIsFoldedIntoTheBound(t *testing.T) {
	shown := map[string]bool{"a.go": true, "b.go": true, "c.go": true}
	got, note := repairPartition(shown, []Cohort{
		{Name: "one", Files: []string{"a.go"}},
		{Name: "two", Files: []string{"b.go"}},
		{Name: "three", Files: []string{"c.go"}},
	}, 2)
	if len(got) != 2 {
		t.Fatalf("the bound must hold: %+v", got)
	}
	var files int
	for _, c := range got {
		files += len(c.Files)
	}
	if files != 3 {
		t.Errorf("folding must keep every file: %+v", got)
	}
	if !strings.Contains(note, "bound") {
		t.Errorf("the fold must be said out loud: %q", note)
	}
}

// A change too small to partition is one cohort whatever the flag says:
// splitting four files into six groups spends six calls to review four files.
func TestASmallChangeIsOneCohort(t *testing.T) {
	opts := Options{Cohorts: 6, MinCohortFiles: 3}
	if got := cohortBound(opts, exploreInput()); got != 1 {
		t.Errorf("a one-file change must be one cohort, got %d", got)
	}
	if got := cohortBound(opts, stagedInput()); got != 5 {
		t.Errorf("five shown files cannot make six cohorts, got %d", got)
	}
	opts.Cohorts = 2
	if got := cohortBound(opts, stagedInput()); got != 2 {
		t.Errorf("the bound is the caller's, got %d", got)
	}
}

// A cohort that fails is counted, not fatal: a merge over two of three is
// worth more than no review, and nothing downstream can tell it happened
// unless the result says so.
func TestAFailedCohortIsCountedAndTheRestAreKept(t *testing.T) {
	// Which call fails is decided by what it asks for, not by arrival order:
	// the cohort calls go out together and a script indexed by turn would
	// fail whichever one happened to be third.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.Contains(string(body), "### The cohorts"):
			fmt.Fprint(w, anthropicSSE("tool_use", 10, 5,
				anthropicToolUse(0, "t0", StageCohorts, partitionBody)))
		case strings.Contains(string(body), "Your cohort: api"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			fmt.Fprint(w, anthropicSSE("tool_use", 10, 5, anthropicToolUse(0, "t1", StageFindings,
				cohortFindings("internal/queue/q.go", "the retry loses the entry"))))
		}
	}))
	t.Cleanup(srv.Close)

	opts := stagedOpts(&exploreAPI{srv: srv})
	var said []string
	opts.Progress = func(s string) { said = append(said, s) }
	res, err := Run(context.Background(), stagedInput(), opts)
	if err != nil {
		t.Fatalf("one cohort of three failing must not fail the review: %v", err)
	}
	if res.CohortsFailed != 1 {
		t.Errorf("the failure must be counted, got %d", res.CohortsFailed)
	}
	if res.Turns != 2 {
		t.Errorf("two cohorts answered, got %d", res.Turns)
	}
	if len(res.Review.Comments) != 1 {
		t.Fatalf("the cohorts that answered are the review: %+v", res.Review.Comments)
	}
	var told bool
	for _, s := range said {
		if strings.Contains(s, "(api) failed") {
			told = true
		}
	}
	if !told {
		t.Errorf("a paid call that did not answer must be said out loud: %q", said)
	}
}

// The catalogue carries what the run's shape can ask for and nothing else.
//
// Not tidiness: five strict tools got a 400 from the endpoint - "the compiled
// grammar is too large ... reduce the number of strict tools" - on a real
// staged run. What the cache needs is that the array does not move between
// two calls of one run, which is what this checks, and a one-shot run has no
// use for the partition contract.
func TestTheCatalogueCarriesOnlyTheShapesContracts(t *testing.T) {
	names := func(opts Options) []string {
		var out []string
		for _, tool := range stageTools(opts) {
			out = append(out, tool.Name)
		}
		return out
	}
	// Reachable is about the calls this run will make, not only its pipeline.
	// A one-shot run with the checking pass on makes two calls and carries
	// both contracts; with it off the ruling is a stage nothing can be pinned
	// to, and its schema is 443 input tokens on the only call there is.
	oneshot := names(Options{Verify: true})
	if !slices.Equal(oneshot, []string{StageReview, StageRuling}) {
		t.Errorf("a verified one-shot run reaches the review and the ruling, got %v", oneshot)
	}
	if plain := names(Options{}); !slices.Equal(plain, []string{StageReview}) {
		t.Errorf("a run with no checking pass reaches the review alone, got %v", plain)
	}
	// The array the endpoint accepts, exactly. review + ruling + findings +
	// cohorts is the one that got the 400; this is what was probed and
	// answered 200, and the fallback call reassembles under the one-shot
	// shape to get the review contract it needs.
	staged := names(Options{Cohorts: 6})
	if !slices.Equal(staged, []string{StageRuling, StageFindings, StageCohorts}) {
		t.Errorf("a staged run carries the three contracts it can ask for, got %v", staged)
	}
	if slices.Contains(names(Options{Synopsis: true}), StageCohorts) {
		t.Error("a run with no fan-out cannot ask for a partition, so it must not carry that contract")
	}
	// Every call of one staged run sends the same array, which is the whole
	// invariant the cache rests on.
	first, err := json.Marshal(stageTools(Options{Cohorts: 6}))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, err := json.Marshal(stageTools(Options{Cohorts: 6}))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("the catalogue serialized differently between two calls of one shape")
		}
	}
}

// A staged row is a describing call plus one per cohort; a one-shot row is
// one call. Averaged together they report a price nobody was charged, and
// MedianOutput - which prices the next review - is worse than meaningless:
// it would quote five calls' output for one.
func TestTheLedgerNeverAveragesAcrossShapes(t *testing.T) {
	entries := []Entry{
		{Model: "claude-sonnet-5", CostUSD: 0.10, Known: true, Usage: Usage{OutputTokens: 700}},
		{Model: "claude-sonnet-5", CostUSD: 0.12, Known: true, Usage: Usage{OutputTokens: 800},
			Pipeline: PipelineOneShot},
		{Model: "claude-sonnet-5", CostUSD: 0.90, Known: true, Usage: Usage{OutputTokens: 4000},
			Pipeline: PipelineStaged, Cohorts: 5},
	}
	byShape := SummarizeByShape(entries)
	one, staged := byShape[PipelineOneShot], byShape[PipelineStaged]
	// The row written before the field existed is a one-shot row: it was one.
	if one.Count != 2 {
		t.Errorf("a row with no shape is a one-shot row, got %d in the set", one.Count)
	}
	if one.Mean > 0.12 {
		t.Errorf("the staged row leaked into the one-shot mean: $%.4f", one.Mean)
	}
	if staged.Count != 1 || staged.Mean < 0.9 {
		t.Errorf("the staged set is its own: %d row(s) at $%.4f", staged.Count, staged.Mean)
	}
	if one.ExpectedOutput() >= 4000 {
		t.Errorf("a one-shot call must not be priced at a fan-out's output: %d", one.ExpectedOutput())
	}
	if s := one.String(); !strings.HasPrefix(s, PipelineOneShot+":") {
		t.Errorf("a shape's line must say which shape it is: %q", s)
	}
}

// The shape is read off the options, not set: one cohort is the unsplit
// review, more is the split, and stepwise is its own. The default is one.
func TestTheShapeIsReadOffCohortsAndStepwise(t *testing.T) {
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{}, PipelineOneShot},
		{Options{Cohorts: 1}, PipelineOneShot},
		{Options{Cohorts: 2}, PipelineStaged},
		{Options{Stepwise: true}, PipelineStepwise},
	} {
		if got := tc.o.withDefaults().Shape(); got != tc.want {
			t.Errorf("%+v: shape %q, want %q", tc.o, got, tc.want)
		}
	}
	if d := (Options{}).withDefaults(); d.Cohorts != 1 {
		t.Errorf("the default is no split, got %d cohort(s)", d.Cohorts)
	}
	if d := (Options{Cohorts: 3, Synopsis: true}).withDefaults(); d.Synopsis {
		t.Error("a split describes the change itself, so Synopsis must be cleared under it")
	}
}
