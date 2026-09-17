package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The prompts are files now, embedded at build time. go:embed fails the build
// on a file that is missing and says nothing about one that is empty, so that
// is checked here: an empty prompt ships a reviewer with no instructions and
// nothing else would notice.
func TestEveryPromptIsLoaded(t *testing.T) {
	for _, p := range []struct {
		name string
		text string
	}{
		{"system.md", systemPrompt},
		{"judging.md", judgingTail},
		{"describing.md", describingTail},
		{"synopsis.md", synopsisPrompt},
		{"note.md", notePrompt},
		{"ruling.md", rulePrompt},
		{"explore-addendum.md", exploreAddendum},
	} {
		if strings.TrimSpace(p.text) == "" {
			t.Errorf("%s loaded empty; the stage that sends it would have no instruction", p.name)
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
