package eval

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/findings"
)

// Two directions on the label set itself, both offline and free.
//
// Every paid number this package produces is a count of labels matched. That
// count is only worth reading if a label fires when a correct review states
// the defect and stays silent when one does not, and neither half was
// asserted over the whole set: labels_test checks both on one fixture, with
// reviews written by hand for it, which does not scale to the twenty the
// fixture set is aiming at.
//
// So these two tests ask the mechanical versions of the same questions. The
// null pushes a padding reviewer through every fixture and expects nothing to
// match. The oracle builds, for each label, the least specific comment that
// label describes, and expects it to match. A label that fails the first is
// too loose to measure with; one that fails the second can never fire, so the
// recall ceiling is below the denominator and nobody is told.

// paddingBodies is what a reviewer that has nothing to say still says.
//
// Written to be plausible rather than empty. An empty review trivially catches
// nothing and proves only that the matcher reads the body at all; the useful
// null is the register a padding reviewer actually produces, because that is
// what a configuration tuned for recall degenerates into. Each line names a
// real reviewing topic and no defect.
//
// No defect, and that is a constraint on what can go in here rather than a
// description of it. A line reading "worth regenerating" was in this list and
// matched stale-generated-file, which is correct behaviour by the label: that
// fixture's whole defect is a generated file nobody regenerated, so the line
// was a finding on it and not padding at all. A null corpus has to be null on
// every fixture, so a line that names any one fixture's defect belongs in that
// fixture's expectations instead.
//
// The lesson generalises past the test. The reason that line read as padding
// is that "consider regenerating this" is something reviewers say reflexively,
// and stale-generated-file is the known-gap fixture, where nothing shipping
// can catch the defect. A reviewer with the reflex scores the gap without
// having found anything. The label is tight enough now that it takes the
// specific claim, and that is worth keeping tight.
//
// Eight lines is also a floor rather than a proof: labels are tightened until
// they survive this corpus, so the corpus is the thing they are fitted to.
// Widening it is how the floor rises.
var paddingBodies = []string{
	"Consider handling the error case here rather than letting it propagate.",
	"This looks correct, but it might be worth adding a guard for the nil case.",
	"The naming here could be clearer, and the order of these checks may matter.",
	"Worth confirming this is covered by a test; the added lines look untested.",
	"This could potentially panic on an empty value. Consider a length check.",
	"The default behaviour changed here, which may affect existing callers.",
	"Consider whether this should be a constant rather than repeated twice.",
	"The comment here explains what the code does rather than why it does it.",
}

// TestAPaddingReviewCatchesNothing is the null baseline.
//
// A generic review is filed against every file the fixture shows, so a
// file-pinned label gets every chance its own location gives it. Anything
// matched is a label whose words are common enough that a reviewer can reach
// it without finding anything, and the fraction of the denominator those
// labels hold is a floor under every recall number the set reports.
func TestAPaddingReviewCatchesNothing(t *testing.T) {
	fx := load(t)
	var falseHits []string
	var totalExpect int

	for _, f := range fx {
		totalExpect += len(f.Annotation.Expect)
		if f.Session == nil || f.Session.Change == nil {
			continue
		}
		var rev findings.Review
		for _, cf := range f.Session.Change.Files {
			if cf.Diff == "" && cf.Head == "" {
				continue
			}
			for _, body := range paddingBodies {
				rev.Comments = append(rev.Comments, findings.ReviewComment{
					File: cf.Path, Line: 1, Body: body,
				})
			}
		}
		if len(rev.Comments) == 0 {
			continue
		}
		sc := Score(f, rev)
		for _, key := range sc.Caught {
			falseHits = append(falseHits, f.Annotation.Name+"/"+key)
		}
	}

	sort.Strings(falseHits)
	t.Logf("null baseline: %d of %d labels matched by a review naming no defect",
		len(falseHits), totalExpect)
	if len(falseHits) > 0 {
		t.Errorf("labels reachable without finding anything (%d/%d):\n  %s",
			len(falseHits), totalExpect, strings.Join(falseHits, "\n  "))
	}
}

// TestEveryLabelIsReachable is the oracle direction.
//
// For each label it builds the least specific comment that label describes:
// the file it pins, the words it asks for, and, for a correlation, references
// resolved out of the fixture's own findings. If that does not match, nothing
// a review could write would, and the label is dead weight in the denominator
// rather than a defect the reviewer missed.
//
// This is mechanical rather than a hand-written correct review, so it proves
// reachability and not correctness. labels_test does the stronger thing for
// one fixture; this does the weaker thing for all of them, which is what
// keeps a label added later from being unmatched forever.
func TestEveryLabelIsReachable(t *testing.T) {
	fx := load(t)
	var dead []string

	for _, f := range fx {
		for _, exp := range f.Annotation.Expect {
			c := oracleComment(f, exp)
			sc := Score(f, findings.Review{Comments: []findings.ReviewComment{c}})
			caught := false
			for _, key := range sc.Caught {
				if key == exp.Key {
					caught = true
					break
				}
			}
			if !caught {
				dead = append(dead, fmt.Sprintf("%s/%s (file=%q correlation=%v relates=%v)",
					f.Annotation.Name, exp.Key, exp.File, exp.Correlation, exp.RelatesToRules))
			}
		}
	}

	if len(dead) > 0 {
		t.Errorf("labels no comment can match (%d):\n  %s", len(dead), strings.Join(dead, "\n  "))
	}
}

// oracleComment is the minimal comment one expectation describes.
func oracleComment(f Fixture, exp Expectation) findings.ReviewComment {
	var b strings.Builder
	b.WriteString("The defect this label names is present: ")
	// AllOf must all appear; AnyOf needs one, and the first is as good as any.
	for _, w := range exp.AllOf {
		b.WriteString(w + " ")
	}
	if len(exp.AnyOf) > 0 {
		b.WriteString(exp.AnyOf[0])
	}

	c := findings.ReviewComment{File: exp.File, Line: 1, Body: b.String()}
	if c.File == "" && f.Session != nil && f.Session.Change != nil {
		for _, cf := range f.Session.Change.Files {
			if cf.Diff != "" || cf.Head != "" {
				c.File = cf.Path
				break
			}
		}
	}
	if !exp.Correlation && len(exp.RelatesToRules) == 0 {
		return c
	}
	c.Category = findings.CategoryCorrelation
	// References resolved out of the fixture's own report, the way a real
	// correlation's would be. A label naming a rule the frozen session does
	// not carry is unreachable for that reason, and this is where it shows.
	if f.Session != nil {
		want := map[string]bool{}
		for _, r := range exp.RelatesToRules {
			want[r] = true
		}
		for _, fi := range f.Session.Report.Findings {
			if len(want) == 0 || want[fi.Rule] {
				c.RelatedFindings = append(c.RelatedFindings, fi.ID)
			}
		}
	}
	if len(c.RelatedFindings) == 0 {
		c.RelatedFindings = []string{"unresolvable"}
	}
	return c
}
