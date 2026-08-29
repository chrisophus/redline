package report

import (
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
