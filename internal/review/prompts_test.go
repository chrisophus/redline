package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The prompts are files now, embedded at build time. go:embed fails the build
// on a file that is missing, and says nothing about one that is empty or that
// lost its leading blank line, so those are checked here: an empty prompt
// ships a reviewer with no instructions and nothing else would notice.
func TestEveryPromptIsLoaded(t *testing.T) {
	for _, p := range []struct {
		name string
		text string
		// leads and trails are the whitespace the concatenation depends on.
		// These strings are joined onto a packet, not rendered on their own,
		// and a stripped newline runs a heading into the line above it.
		leads, trails string
	}{
		{"system.md", systemPrompt, "You are", "\n"},
		{"judging.md", judgingTail, "\n", "\n"},
		{"describing.md", describingTail, "\n", "\n"},
		// The short prompt is the whole system block and nothing follows it, so
		// it is the one file that needs no trailing newline.
		{"brief.md", briefPrompt, "You are", "."},
		{"oneshot-addendum.md", oneShotAddendum, "\n\n", "."},
		{"synopsis.md", synopsisPrompt, "\n\n", "."},
		{"stepwise.md", stepwisePrompt, "\n\n", "."},
		{"stepwise-lead.md", stepwiseLead, "##", "\n\n"},
		{"findings.md", findingsPrompt, "\n\n", "."},
		{"ruling.md", rulePrompt, "\n", ""},
		{"explore-addendum.md", exploreAddendum, "\n\n", "."},
	} {
		if strings.TrimSpace(p.text) == "" {
			t.Errorf("%s loaded empty; the stage that sends it would have no instruction", p.name)
			continue
		}
		if !strings.HasPrefix(p.text, p.leads) {
			t.Errorf("%s starts %q, want it to start %q: it is concatenated, not rendered alone",
				p.name, p.text[:min(12, len(p.text))], p.leads)
		}
		if p.trails != "" && !strings.HasSuffix(p.text, p.trails) {
			t.Errorf("%s ends %q, want it to end %q", p.name, p.text[max(0, len(p.text)-12):], p.trails)
		}
	}
}

// Every file in the directory is reachable from Go. A prompt nobody embeds is
// a prompt somebody will edit expecting it to matter.
func TestNoPromptFileIsOrphaned(t *testing.T) {
	entries, err := os.ReadDir("prompts")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("prompt.go")
	if err != nil {
		t.Fatal(err)
	}
	rule, err := os.ReadFile("rule.go")
	if err != nil {
		t.Fatal(err)
	}
	explore, err := os.ReadFile("explore.go")
	if err != nil {
		t.Fatal(err)
	}
	all := string(src) + string(rule) + string(explore)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		if !strings.Contains(all, "go:embed prompts/"+e.Name()) {
			t.Errorf("prompts/%s is embedded by nothing", e.Name())
		}
	}
}
