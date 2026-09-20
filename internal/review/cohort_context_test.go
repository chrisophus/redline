package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

const (
	alphaContent = "alpha-only-marker: what surrounds internal/alpha/a.go"
	betaContent  = "beta-only-marker: what surrounds internal/beta/b.go"
)

// cohortContextInput is two files, each with its own resolved context, so a
// test can tell whether a call carries one file's context, the other's,
// both, or neither.
func cohortContextInput() Input {
	return Input{
		Report: &findings.Report{},
		Change: &change.Set{Files: []change.File{
			{Path: "internal/alpha/a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n", Added: 1},
			{Path: "internal/beta/b.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n", Added: 1},
		}},
		Envelopes: []*envelope.Envelope{{
			SchemaVersion: envelope.SchemaVersion,
			Provider:      envelope.Provider{Name: "gorefactor", Language: "go"},
			Expansions: []envelope.Expansion{
				{Role: envelope.RoleEnclosing, File: "internal/alpha/a.go", Content: alphaContent},
				{Role: envelope.RoleEnclosing, File: "internal/beta/b.go", Content: betaContent},
			},
		}},
	}
}

// Under CohortContext the shared prefix carries neither file's context: the
// describing call, which reads that prefix unscoped, would otherwise see the
// whole change's context for a call that judges nothing.
func TestCohortContextLeavesTheSharedPrefixEmpty(t *testing.T) {
	in := cohortContextInput()
	opts := Options{Cohorts: 2, CohortContext: true}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Prompt, alphaContent) || strings.Contains(res.Prompt, betaContent) {
		t.Errorf("the shared prefix carries context under CohortContext: %q", res.Prompt)
	}
	one := res.cohortsRequest(opts, in)
	if strings.Contains(one.Prompt+one.Tail, alphaContent) || strings.Contains(one.Prompt+one.Tail, betaContent) {
		t.Error("the describing call carries context under CohortContext")
	}
}

// Each cohort call gets only the context that belongs to its own files, in
// its own tail, and none of another cohort's.
func TestCohortContextScopesEachCohortToItsOwnFiles(t *testing.T) {
	in := cohortContextInput()
	opts := Options{Cohorts: 2, CohortContext: true}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	alphaCohort := Cohort{Name: "alpha", Summary: "s", Files: []string{"internal/alpha/a.go"}}
	betaCohort := Cohort{Name: "beta", Summary: "s", Files: []string{"internal/beta/b.go"}}
	all := []Cohort{alphaCohort, betaCohort}

	a := res.cohortRequest(opts, in, alphaCohort, all, 0, 2)
	if !strings.Contains(a.Tail, alphaContent) {
		t.Error("the alpha cohort's call does not carry alpha's own context")
	}
	if strings.Contains(a.Tail, betaContent) {
		t.Error("the alpha cohort's call carries beta's context")
	}

	b := res.cohortRequest(opts, in, betaCohort, all, 1, 2)
	if !strings.Contains(b.Tail, betaContent) {
		t.Error("the beta cohort's call does not carry beta's own context")
	}
	if strings.Contains(b.Tail, alphaContent) {
		t.Error("the beta cohort's call carries alpha's context")
	}
}

// Off, a split run still sends the whole change's context on every call,
// unchanged from before CohortContext existed.
func TestWithoutCohortContextEveryCohortSeesTheWholeChange(t *testing.T) {
	in := cohortContextInput()
	opts := Options{Cohorts: 2}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Prompt, alphaContent) || !strings.Contains(res.Prompt, betaContent) {
		t.Error("the shared prefix should carry both files' context without CohortContext")
	}
	alphaCohort := Cohort{Name: "alpha", Summary: "s", Files: []string{"internal/alpha/a.go"}}
	all := []Cohort{alphaCohort}
	a := res.cohortRequest(opts, in, alphaCohort, all, 0, 1)
	if !strings.Contains(a.Prompt, betaContent) {
		t.Error("without CohortContext a cohort call should still see the other cohort's context in the shared prefix")
	}
}

// A caller's expansion is filed under the calling file's own path, not the
// file whose change it calls into. Context whose File belongs to no cohort
// at all - an unchanged caller, most of the caller role - must reach every
// cohort rather than none: excluding only what is definitely another
// cohort's is what buys that, where keeping only what is definitely this
// cohort's own would have dropped it everywhere.
func TestCohortContextKeepsContextWithNoCohortOfItsOwn(t *testing.T) {
	const callerContent = "caller-marker: an unchanged file that calls into internal/alpha/a.go"
	in := cohortContextInput()
	in.Envelopes[0].Expansions = append(in.Envelopes[0].Expansions, envelope.Expansion{
		Role: envelope.RoleCaller, File: "internal/gamma/caller.go", Content: callerContent,
	})
	opts := Options{Cohorts: 2, CohortContext: true}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	alphaCohort := Cohort{Name: "alpha", Summary: "s", Files: []string{"internal/alpha/a.go"}}
	betaCohort := Cohort{Name: "beta", Summary: "s", Files: []string{"internal/beta/b.go"}}
	all := []Cohort{alphaCohort, betaCohort}

	a := res.cohortRequest(opts, in, alphaCohort, all, 0, 2)
	if !strings.Contains(a.Tail, callerContent) {
		t.Error("context with no cohort of its own must still reach the alpha cohort")
	}
	b := res.cohortRequest(opts, in, betaCohort, all, 1, 2)
	if !strings.Contains(b.Tail, callerContent) {
		t.Error("context with no cohort of its own must still reach the beta cohort")
	}
}

// fanOut runs every cohort's call at once, so a room computed against the
// whole remaining ceiling is a room every one of them tries to spend at the
// same time. Dividing it by the fan-out's own bound keeps their combined
// spend inside what the ceiling actually allows.
func TestCohortContextDividesRoomAcrossTheFanOut(t *testing.T) {
	bigContent := "big-marker: " + strings.Repeat("x", 3000)
	in := cohortContextInput()
	in.Envelopes[0].Expansions[0].Content = bigContent
	opts := Options{Cohorts: 2, CohortContext: true}.withDefaults()
	res, err := Assemble(in, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Context is excluded from the shared prefix under CohortContext, so
	// res.InputEstimate holds still whatever the ceiling used to assemble
	// it was; overriding it here changes only the room cohortRequest sees.
	opts.Ceiling = res.InputEstimate + 5000

	alphaCohort := Cohort{Name: "alpha", Summary: "s", Files: []string{"internal/alpha/a.go"}}
	betaCohort := Cohort{Name: "beta", Summary: "s", Files: []string{"internal/beta/b.go"}}
	all := []Cohort{alphaCohort, betaCohort}

	solo := res.cohortRequest(opts, in, alphaCohort, all, 0, 1)
	if !strings.Contains(solo.Tail, bigContent) {
		t.Fatal("the content should fit when this cohort is the only one in the fan-out")
	}
	crowded := res.cohortRequest(opts, in, alphaCohort, all, 0, 8)
	if strings.Contains(crowded.Tail, bigContent) {
		t.Error("a wide fan-out must divide the room, not let each cohort spend the whole remaining ceiling")
	}
}

// --cohort-context needs a partition to scope by, and answers a question
// --defer-context already answers a different way.
func TestCohortContextIsRefusedWithoutAPartitionOrAlongsideDeferContext(t *testing.T) {
	in := cohortContextInput()
	ctx := context.Background()
	if _, err := Run(ctx, in, Options{CohortContext: true}); err == nil {
		t.Error("expected an error for --cohort-context below --cohorts 2")
	}
	if _, err := Run(ctx, in, Options{Cohorts: 2, CohortContext: true, DeferContext: true}); err == nil {
		t.Error("expected an error combining --cohort-context with --defer-context")
	}
}
