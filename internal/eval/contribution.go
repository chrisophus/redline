package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/review"
)

// Whether the context envelope is worth its cost is the central claim of the
// whole provider layer, and it is not something to take on faith. This file
// measures it.
//
// The measurable half is whether the envelope carries information the diff
// does not. That is computable offline, for free, on every fixture: an
// expansion whose every line is already in the diff section tells the model
// nothing and costs budget to send. The other half, whether that information
// changes what a reviewer concludes, needs a model and is measured by the
// paid sweep.
//
// Keeping the first number in the test suite means a provider that regresses
// into echoing the diff back shows up as a number rather than as a suspicion.

// Contribution is what one fixture's envelope adds beyond its diff.
type Contribution struct {
	Fixture string
	// Expansions the provider produced, and how many survived deduplication
	// against the diff.
	Produced int
	Sent     int
	// Redundant is what was dropped because the diff already showed it.
	Redundant int
	// RedundantTokens is what sending it would have cost.
	RedundantTokens int
	// ByRole counts what was sent, per role.
	ByRole map[envelope.Role]int
}

// Informative is the share of the provider's output that survived, which is
// the share that carried something the diff did not.
func (c Contribution) Informative() float64 {
	if c.Produced == 0 {
		return 0
	}
	return float64(c.Sent) / float64(c.Produced)
}

// Contributions measures every fixture.
func Contributions(fixtures []Fixture) []Contribution {
	out := make([]Contribution, 0, len(fixtures))
	for _, f := range fixtures {
		in := review.Input{
			Report: &f.Session.Report, Change: f.Session.Change,
			Envelopes: f.Session.Envelopes, Absent: f.Session.ContextAbsent,
		}
		res, err := review.Assemble(in, review.Options{})
		if err != nil {
			continue
		}
		c := Contribution{Fixture: f.Annotation.Name, ByRole: map[envelope.Role]int{}}
		for _, e := range f.Session.Envelopes {
			if e != nil {
				c.Produced += len(e.Expansions)
			}
		}
		c.Sent = len(res.Budget.Kept)
		c.Redundant = res.Budget.Redundant
		c.RedundantTokens = res.Budget.RedundantTokens
		for _, x := range res.Budget.Kept {
			c.ByRole[x.Role]++
		}
		out = append(out, c)
	}
	return out
}

// Report renders the contribution table.
func ContributionReport(cs []Contribution) string {
	var b strings.Builder
	b.WriteString("| Fixture | produced | sent | redundant | tokens saved | roles sent |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, c := range cs {
		roles := make([]string, 0, len(c.ByRole))
		for r, n := range c.ByRole {
			roles = append(roles, fmt.Sprintf("%s=%d", r, n))
		}
		sort.Strings(roles)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s |\n",
			c.Fixture, c.Produced, c.Sent, c.Redundant, c.RedundantTokens,
			strings.Join(roles, " "))
	}
	return b.String()
}
