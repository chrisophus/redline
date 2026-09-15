package envelope

import "testing"

// A deletion's history names the lines it deleted, and those line numbers can
// fall inside what the diff shows. Its content is commits, so the diff must
// not make it redundant the way it makes a type definition over the same lines
// redundant.
func TestRemovalIsNeverMadeRedundantByTheDiff(t *testing.T) {
	seen := Seen{}
	for line := 1; line <= 10; line++ {
		seen.Add("a.go", line)
	}
	e := &Envelope{Expansions: []Expansion{
		{Role: RoleRemoval, File: "a.go", StartLine: 2, EndLine: 4,
			Content: "commit abc\n\n    add the nil guard after the retry path panicked\n"},
		{Role: RoleType, File: "a.go", StartLine: 2, EndLine: 4, Content: "type T struct{}\n"},
	}}
	b := FitFilter(e, 100000, seen, Filter{})
	var kept []Role
	for _, x := range b.Kept {
		kept = append(kept, x.Role)
	}
	if len(kept) != 1 || kept[0] != RoleRemoval {
		t.Errorf("kept %v, want the removal alone: every line both name was shown, "+
			"and only the type's content is those lines", kept)
	}
	if RoleRemoval.Gloss() == "" {
		t.Error("removal has no description for the model")
	}
}
