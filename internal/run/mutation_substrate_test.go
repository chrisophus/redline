package run

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/mutation"
)

func TestMutationSubstrate(t *testing.T) {
	// A report with survivors: ran, no clean confirmation.
	st, conf := mutationSubstrate(&mutation.Result{Report: "mutants.json", Killed: 3, Lived: 2}, true, true)
	if st.State != findings.SubstrateRan {
		t.Fatalf("ran with survivors: state = %q", st.State)
	}
	if conf != nil {
		t.Fatalf("survivors present, no all-killed confirmation expected: %+v", conf)
	}

	// Every mutant killed: ran, with the confirmation.
	st, conf = mutationSubstrate(&mutation.Result{Report: "mutants.json", Killed: 4, Lived: 0}, true, true)
	if st.State != findings.SubstrateRan || conf == nil || conf.Rule != "mutation-all-killed" {
		t.Fatalf("all killed: state=%q conf=%+v", st.State, conf)
	}

	// Not configured: not-applicable, which renders nowhere.
	if st, _ := mutationSubstrate(nil, false, true); st.State != findings.SubstrateNotApplicable {
		t.Fatalf("not configured: state = %q, want not-applicable", st.State)
	}

	// Configured but no diff-scoped result: skipped, stated.
	if st, _ := mutationSubstrate(nil, true, true); st.State != findings.SubstrateSkipped {
		t.Fatalf("configured but empty: state = %q, want skipped", st.State)
	}
}

// A run where mutants failed on the runner has not established that anything
// was caught, so the all-killed confirmation must not fire and the detail has
// to say the run was incomplete.
func TestInfraErrorsWithholdTheAllKilledConfirmation(t *testing.T) {
	res := &mutation.Result{Report: "mutants.json", Killed: 4, Lived: 0, Infra: 2}
	st, conf := mutationSubstrate(res, true, true)
	if conf != nil {
		t.Fatalf("mutants that never ran cannot confirm the lines are asserted: %+v", conf)
	}
	if !strings.Contains(st.Detail, "2 could not be run") {
		t.Fatalf("detail = %q; the failed runs have to be visible", st.Detail)
	}
}

func TestInfraErrorsBecomeAnUnknown(t *testing.T) {
	res := &mutation.Result{Report: "mutants.json", Killed: 1, Infra: 1,
		Unreliable: []mutation.FileSurvivors{{Path: "a.go", Mutants: []mutation.Mutant{{Line: 7}}}}}
	u := mutationUnknown(res)
	if u == nil {
		t.Fatal("a line nobody measured is an unknown, not a pass")
	}
	if !strings.Contains(u.Message, "a.go:7") || !strings.Contains(u.Message, "not killed") {
		t.Fatalf("unknown = %q", u.Message)
	}
	if mutationUnknown(&mutation.Result{Killed: 3}) != nil {
		t.Fatal("a clean run must not manufacture an unknown")
	}
}
