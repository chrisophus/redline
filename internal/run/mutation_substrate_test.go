package run

import (
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
