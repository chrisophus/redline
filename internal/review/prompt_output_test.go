package review

import (
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/envelope"
)

// The header introduces the context block by naming what is in it. A provider
// is allowed to ship a role Redline does not rank — the graph adapter's
// `neighbor` — and the sentence used to enumerate the six known roles
// unconditionally, so such a block arrived under a sentence stating every
// block was one of six other things it was not.
func TestTheContextHeaderNamesOnlyTheRolesPresent(t *testing.T) {
	in := Input{}
	got := in.contextHeader([]envelope.Role{envelope.RoleHistory})
	if !strings.Contains(got, "prior history of these lines") {
		t.Errorf("the header does not name the role it carries:\n%s", got)
	}
	// The roles that are not in the block must not be claimed.
	for _, absent := range []string{"a caller of something", "a sibling implementation", "a test that covers"} {
		if strings.Contains(got, absent) {
			t.Errorf("the header claims %q for a block that has no such role:\n%s", absent, got)
		}
	}
	// An unranked role is named, and left to the provider's own note rather
	// than glossed by Redline, which does not know what it means.
	got = in.contextHeader([]envelope.Role{envelope.Role("neighbor"), envelope.RoleHistory})
	if !strings.Contains(got, "neighbor") {
		t.Errorf("an unranked role is not named at all:\n%s", got)
	}
	if !strings.Contains(got, "prior history of these lines") {
		t.Errorf("an unranked role displaced a known one:\n%s", got)
	}
	// With nothing to describe there is no sentence to write.
	if got := in.contextHeader(nil); strings.Contains(got, "Each block says what it is") {
		t.Errorf("the header claims to describe blocks it has none of:\n%s", got)
	}
}
