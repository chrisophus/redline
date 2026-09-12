package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
)

// cmdPostmortem reads back what the last review did.
//
// It calls nothing and observes nothing: the trace was written when the
// review ran, and this renders it. A review is three stages and only the last
// one leaves a file behind, so the question people actually have afterwards,
// whether the findings that never reached them were wrong or were never
// checked, has had no way to be answered.
func cmdPostmortem(o opts) error {
	t, err := postmortem.Load(o.out)
	if err != nil {
		return err
	}
	// A trace outlives the session beside it, the same way review.json does,
	// and the same mistake is available: reading the postmortem of one change
	// while looking at another. Said rather than refused, because a trace of
	// the previous change is still what someone asking about the previous
	// change wants.
	if res, lerr := run.LoadSession(o.out); lerr == nil && t.Revision != "" {
		if id := change.ReviewIdentity(res.Report.BaseSHA, res.Change); id != t.Revision {
			fmt.Fprintf(os.Stderr,
				"redline: this is the review of %s, and %s now holds a run of a different change; "+
					"`redline review` again to look back on that one\n", t.Target, o.out)
		}
	}
	if o.format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(t)
	}
	_, err = fmt.Print(t.Render())
	return err
}
