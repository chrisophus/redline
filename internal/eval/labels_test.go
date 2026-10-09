package eval

// A label is a measuring instrument, and a broken one is worse than a missing
// one: an expectation whose file path has a typo, or whose any_of contains
// nothing a reviewer would plausibly write, scores a correct review as a miss
// and makes every configuration look worse than it is. The inverse matters
// too: an any_of holding the change's own vocabulary rather than the defect's
// credits approving prose with a catch. Both directions are checked here
// without spending anything, against a review written by hand.
//
// This caught a real one: `||` in the combined-guard label matched a comment
// that called the widening sensible.

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// The files this fixture's change touches, named once because three tests
// write reviews against them.
const (
	nilWalk      = "cmd/gorefactor/cmd_lint_unnecessary_nil_check.go"
	nilFacts     = "cmd/gorefactor/cmd_lint_unnecessary_nil_check_facts.go"
	nilGuard     = "cmd/gorefactor/cmd_lint_redundant_nil_guard.go"
	nilWalkTest  = "cmd/gorefactor/cmd_lint_unnecessary_nil_check_test.go"
	nilRegistry  = "cmd/gorefactor/cmd_lint.go"
	nilChangelog = "CHANGELOG.md"
)

func nilRulesFixture(t *testing.T) Fixture {
	t.Helper()
	for _, f := range load(t) {
		if f.Annotation.Name == "gorefactor-nil-rules" {
			return f
		}
	}
	t.Fatal("fixture gorefactor-nil-rules did not load")
	return Fixture{}
}

func nilComment(file, body string) findings.ReviewComment {
	return findings.ReviewComment{File: file, Line: 1, Body: body, Severity: "warning"}
}

// parityCorrelation is the one label that cannot be written as prose alone: a
// correlation scores only when its references resolve to the rules the label
// names, so the comment carries the session's own sibling-missing-file ids.
func parityCorrelation(f Fixture) findings.ReviewComment {
	c := nilComment(nilWalk, "The three parity notices are one fact: they are the implementation, facts and test files of a single new lint rule, and no other cmd_lint_ rule has a counterpart outside this directory either.")
	c.Category = findings.CategoryCorrelation
	for _, fi := range f.Session.Report.Findings {
		if fi.Rule == "sibling-missing-file" {
			c.RelatedFindings = append(c.RelatedFindings, fi.ID)
		}
	}
	return c
}

// nilRulesCorrectReview is what a reviewer that found every labelled defect
// would have written: one comment per defect, in a reviewer's own words rather
// than the label's.
//
// Shared, because it is the only review in this repository known to be
// entirely correct, which makes it the control for anything that claims to
// remove noise. A suppression that touches one of these is removing signal.
// The correlation label needs the session's own finding ids, so the fixture is
// passed in rather than the ids written down: an id copied into this file
// would keep matching after the session it names stopped containing it.
func nilRulesCorrectReview(f Fixture) findings.Review {
	c := nilComment
	walk, facts, guard, walkTest := nilWalk, nilFacts, nilGuard, nilWalkTest
	return findings.Review{Comments: []findings.ReviewComment{
		c(facts, "checkExpr uses ast.Inspect, which walks into a function literal's body, and the statement walk never registers the literal's parameters. A closure parameter that shadows an outer guarded name is judged against the outer fact, so a mandatory check is reported as impossible."),
		c(walk, "The condition is checked and only then invalidated: checkExpr runs before invalidateNilFacts, so a mutating call in the left conjunct is ignored and the report is emitted against a fact the condition destroys."),
		c(guard, "The prologue scan now continues past an AssignStmt, so hasEntryNilGuard accepts a guard on a parameter that was rebind by an earlier statement; the guarded value is no longer the caller's argument."),
		c(guard, "The message reports the func keyword's line rather than the guard's, so two findings about different guards both say the same line."),
		c(guard, "A combined a == nil || b == nil guard counts for each name separately, but the advice is to drop the guard, which still protects the parameter that was never proven. Each disjunct is treated as its own guard."),
		c(guard, "precedingNilReject accepts a guard from an earlier statement even when the argument is reassigned before the call, so a write between the guard and the call is ignored."),
		c(facts, "The address-of branch of invalidateNilFacts is a no-op: closureTaintedNames already taints any name whose address is taken, and no fact is ever established for a tainted name."),
		c(walkTest, "This test cannot fail: the variable is package-level, and globals are never tracked, so the silence has nothing to do with the intervening call."),
		c(walkTest, "The fixture contains a label, and the predicate returns on any label, so the goto arm is never the deciding factor."),
		c(walkTest, "Nothing asserts the issue's file field, so the reported path:line location is unverified."),
		c(walkTest, "No test assigns a slice literal or a map literal, so the composite literal branch is uncovered."),
		c(guard, "No test exercises a caller-side x != nil conjunct, so enclosingNonNilGuard is never run."),
		// The two labels whose evidence is outside the diff. A reviewer can
		// only write these having read files this change does not touch.
		c(facts, "The len-combo check reports the same defect as staticcheck S1009, which .golangci.yml already enables, so doctor counts one line twice."),
		c(guard, "argProvenNonNil rejects anything that is not a bare identifier, and every guarded helper in this repository is called with a selector, so the widened proofs are inert here and the rule fires nowhere in the tree."),
		// Labelled from measured samples rather than from reading the diff,
		// which is why they sit apart: the first arm wrote all three and all
		// three scored as extras.
		c(nilRegistry, "This registers a warning-tier rule and widens another without touching the committed lint baseline. Every finding the new rules produce on gorefactor's own source is new against that baseline, so the one-way ratchet fails the gate; both earlier rule-adding commits re-locked it in the same commit."),
		c(nilChangelog, "The entry says facts are dropped on closure capture, but closureTaintedNames only taints a name assigned inside a function literal or whose address is taken, so a name merely captured and read keeps its fact."),
		parityCorrelation(f),
	}}
}

func TestNilRulesLabelsMatchACorrectReviewAndNothingElse(t *testing.T) {
	fixture := nilRulesFixture(t)
	target := &fixture
	c := nilComment
	walk, guard := nilWalk, nilGuard
	rev := nilRulesCorrectReview(fixture)

	sc := Score(*target, rev)
	if len(sc.Missed) > 0 {
		t.Errorf("labels that a correct review does not match: %v", sc.Missed)
	}
	if len(sc.Caught) != len(target.Annotation.Expect) {
		t.Errorf("caught %d of %d labels", len(sc.Caught), len(target.Annotation.Expect))
	}
	t.Logf("caught=%d missed=%v extra=%d", len(sc.Caught), sc.Missed, sc.Extra)

	// And the inverse: prose about the change that names no defect must not
	// score, or the labels are matching vocabulary rather than findings.
	vague := findings.Review{Comments: []findings.ReviewComment{
		c(walk, "This adds a new lint rule that walks each function and tracks whether a value was constructed non-nil, then reports checks that cannot fire. The tests cover the main shapes."),
		c(guard, "The guard rule now understands || chains and && conjuncts, which is a sensible widening, and the caller-side proofs look correct."),
	}}

	if sc := Score(*target, vague); len(sc.Caught) > 0 {
		t.Errorf("approving prose scored as catching %v", sc.Caught)
	}
}

// A with/without-context comparison is only meaningful if some label cannot be
// reached from the diff. The first twelve labels here were authored by reading
// the changed files, so not one of them names an unchanged file, and the arms
// run against them measured the cost of context without ever offering it
// anything to earn. That is a property of the label set, not a finding about
// providers, and it is invisible unless something asserts it.
//
// Two kinds qualify. Labels keyed `needs-unchanged-file` need a file the
// change does not touch; labels marked `needs_history` need a commit body the
// history role carries. The measured arms bear the distinction out: the
// history-only label was caught by the arm with context and missed by the arm
// without it. This fails if the class is emptied.
func TestSomeLabelsCannotBeReachedFromTheDiffAlone(t *testing.T) {
	fx := load(t)
	for _, f := range fx {
		if f.Annotation.Name != "gorefactor-nil-rules" {
			continue
		}
		changed := map[string]bool{}
		for _, cf := range f.Session.Change.Files {
			changed[cf.Path] = true
		}
		var beyond int
		for _, e := range f.Annotation.Expect {
			if !strings.Contains(e.Key, "needs-unchanged-file") && !e.NeedsHistory {
				continue
			}
			beyond++
			// Such a label must not be pinned to a changed file, or the
			// match is decided by the diff after all.
			if e.File != "" && changed[e.File] {
				t.Errorf("%s claims evidence outside the diff but is pinned to changed file %s",
					e.Key, e.File)
			}
		}
		if beyond == 0 {
			t.Error("no label needs a file outside the diff, so a context arm scored here " +
				"can only measure what context costs, never what it is worth")
		}
		return
	}
	t.Fatal("fixture gorefactor-nil-rules did not load")
}
