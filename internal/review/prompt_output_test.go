package review

import (
	"strings"
	"testing"
)

// The schema requires an overview and a line per file, and the report renders
// both: markdown puts the overview under "Review (agent)" and appends each
// summary to its file, and the HTML drill-in shows them per file. Nothing in
// the prompt asked for either, so the model was filling two required fields
// with no idea what they were for.
func TestThePromptAsksForTheOverviewAndTheFileSummaries(t *testing.T) {
	sys := systemPrompt
	for _, want := range []string{
		"The overview is one or two paragraphs",
		"The files array is one line per file",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("the prompt does not ask for it: %q", want)
		}
	}
	// The rules about what is not worth reporting are about comments. Applied
	// to the summaries they would argue for leaving them empty, since a
	// summary is by definition a summary of what the diff shows.
	if !strings.Contains(sys, "are not findings") {
		t.Error("the prompt does not separate the summaries from the finding rules")
	}
	// A file whose diff was held back cannot be summarised without inventing.
	if !strings.Contains(sys, "none for the ones held back") {
		t.Error("the prompt does not stop the model summarising files it was not shown")
	}
}
