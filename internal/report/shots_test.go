package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
)

// 1×1 transparent PNG.
var png1x1 = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestMaterializeShotsEmbedsDataURI(t *testing.T) {
	src := filepath.Join(t.TempDir(), "page.png")
	if err := os.WriteFile(src, png1x1, 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	shots, err := MaterializeShots(out, []packet.Shot{{
		Route: "/threads", Path: src, Caption: "reply form",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 1 || shots[0].Route != "/threads" || shots[0].Before != "" {
		t.Fatalf("%+v", shots)
	}
	if !strings.HasPrefix(string(shots[0].After), "data:image/png;base64,") {
		t.Fatalf("expected data URI, got %s", shots[0].After[:min(40, len(shots[0].After))])
	}
	copied, err := filepath.Glob(filepath.Join(out, "evidence", "ui", "*"))
	if err != nil || len(copied) != 1 {
		t.Fatalf("expected one copied file, got %v %v", copied, err)
	}
}

func TestHTMLRendersAgentWalkNotEmptyPane(t *testing.T) {
	src := filepath.Join(t.TempDir(), "page.png")
	if err := os.WriteFile(src, png1x1, 0o644); err != nil {
		t.Fatal(err)
	}
	shots, err := MaterializeShots(t.TempDir(), []packet.Shot{{Route: "/home", Path: src}})
	if err != nil {
		t.Fatal(err)
	}
	html, err := HTML(HTMLInput{
		Report:      &findings.Report{Coverage: findings.Coverage{ChangedFiles: 1, ExaminedFiles: 0}},
		Screenshots: shots,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "The UI pane did not run") {
		t.Fatal("agent shots must not render as the empty-pane message")
	}
	if !strings.Contains(html, "Walked by the reviewing agent") {
		t.Fatal("must label the walk as the agent's, not checks 15–16")
	}
	if strings.Contains(html, "#ZgotmplZ") {
		t.Fatal("data URI was stripped as an unsafe URL")
	}
	if !strings.Contains(html, `alt="/home"`) || !strings.Contains(html, "data:image/png;base64,") {
		t.Fatal("expected embedded screenshot")
	}
}

func TestMaterializeShotsMissingFile(t *testing.T) {
	_, err := MaterializeShots(t.TempDir(), []packet.Shot{{Route: "/", Path: "/no/such.png"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

// Past the whole-report budget, captures are linked from evidence/ui rather
// than inlined: report.html is one document, and base64 inflates every byte
// by a third.
func TestMaterializeShotsStopsInliningPastTheBudget(t *testing.T) {
	dir := t.TempDir()
	src := t.TempDir()
	big := make([]byte, 2<<20)
	var shots []packet.Shot
	for i := 0; i < 8; i++ {
		p := filepath.Join(src, fmt.Sprintf("s%d.png", i))
		if err := os.WriteFile(p, big, 0o644); err != nil {
			t.Fatal(err)
		}
		shots = append(shots, packet.Shot{Route: fmt.Sprintf("/r%d", i), Path: p})
	}
	out, err := MaterializeShots(dir, shots)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(shots) {
		t.Fatalf("dropped captures: %d of %d", len(out), len(shots))
	}
	inlined, linked := 0, 0
	for _, s := range out {
		if strings.HasPrefix(string(s.After), "data:") {
			inlined++
		} else {
			linked++
		}
		if s.File == "" {
			t.Fatal("every capture should name its copy under evidence/ui")
		}
		if _, err := os.Stat(filepath.Join(dir, s.File)); err != nil {
			t.Fatalf("copy missing for %s: %v", s.Route, err)
		}
	}
	if linked == 0 {
		t.Fatalf("nothing was linked; %d inlined, budget not enforced", inlined)
	}
	if inlined == 0 {
		t.Fatal("nothing was inlined; the budget is too tight")
	}
}

func TestPruneMissingShotsKeepsOnlyWhatExists(t *testing.T) {
	src := t.TempDir()
	here := filepath.Join(src, "here.png")
	if err := os.WriteFile(here, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := PruneMissingShots([]packet.Shot{
		{Route: "/a", Path: here, Before: filepath.Join(src, "gone.png")},
		{Route: "/b", Path: filepath.Join(src, "gone.png")},
	})
	if len(got) != 1 || got[0].Route != "/a" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Before != "" {
		t.Fatal("a missing before image should be dropped, not kept")
	}
}
