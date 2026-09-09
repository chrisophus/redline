package change_test

import (
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/target"
)

func worktree(files ...change.File) *change.Set {
	return &change.Set{Files: files}
}

func committed(head string) *change.Set {
	return &change.Set{Target: &target.Target{Head: head}}
}

// A working tree has no head SHA and its content moves under an unchanged
// base, so the diff has to be part of the identity: editing the tree must
// invalidate a review written against it.
func TestReviewIdentityDiffersForWorktreeChanges(t *testing.T) {
	base := "aaaaaaaaaaaaaaaa"
	a := change.ReviewIdentity(base, worktree(change.File{Path: "a.go", Diff: "+one"}))
	b := change.ReviewIdentity(base, worktree(change.File{Path: "a.go", Diff: "+two"}))
	if a == b {
		t.Fatal("different working-tree diffs must not share an identity")
	}
	same := change.ReviewIdentity(base, worktree(change.File{Path: "a.go", Diff: "+one"}))
	if a != same {
		t.Fatal("the same tree against the same base must be the same identity twice")
	}
}

// This is the pair that matters for a stale review: two pull requests off the
// same base are different changes, and a review of one must not read as a
// review of the other.
func TestReviewIdentitySeparatesTwoHeadsOnOneBase(t *testing.T) {
	base := "aaaaaaaaaaaaaaaa"
	first := change.ReviewIdentity(base, committed("bbbbbbbbbbbbbbbb"))
	second := change.ReviewIdentity(base, committed("cccccccccccccccc"))
	if first == second {
		t.Fatal("two heads off one base share an identity, so a review of one would merge onto the other")
	}
	if first != change.ReviewIdentity(base, committed("bbbbbbbbbbbbbbbb")) {
		t.Fatal("the same head must be the same identity twice, or every review reads as stale")
	}
}

// A committed change is identified by its SHAs alone. The files are not
// hashed, so a review survives the session being rebuilt for the same
// revision -- which is what makes the check a staleness test rather than a
// re-run test.
func TestReviewIdentityIgnoresFilesForACommittedChange(t *testing.T) {
	base := "aaaaaaaaaaaaaaaa"
	bare := change.ReviewIdentity(base, committed("bbbbbbbbbbbbbbbb"))
	withFiles := &change.Set{
		Target: &target.Target{Head: "bbbbbbbbbbbbbbbb"},
		Files:  []change.File{{Path: "a.go", Diff: "+one"}},
	}
	if bare != change.ReviewIdentity(base, withFiles) {
		t.Fatal("a committed change's identity moved with its file list")
	}
}

// A different base is a different change even at the same head: a rebase is
// exactly that, and a review written before one describes code that has moved.
func TestReviewIdentityFollowsTheBase(t *testing.T) {
	head := "bbbbbbbbbbbbbbbb"
	if change.ReviewIdentity("aaaaaaaaaaaaaaaa", committed(head)) ==
		change.ReviewIdentity("dddddddddddddddd", committed(head)) {
		t.Fatal("the base is not part of the identity, so a rebase would keep a stale review")
	}
}

func TestReviewIdentityHandlesNoChange(t *testing.T) {
	// Nil is the report rendering before a change is built. It must not panic
	// and must not collide with a real change.
	if change.ReviewIdentity("aaaaaaaaaaaaaaaa", nil) == "" {
		t.Fatal("a nil change must still produce an identity")
	}
}
