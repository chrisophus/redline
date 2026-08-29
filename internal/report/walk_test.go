package report

import (
	"testing"

	"github.com/ccason/redline/internal/packet"
)

func TestFileWalkAttachesNotesInPacketOrder(t *testing.T) {
	rows := fileWalk(
		[]packet.FileChange{
			{Path: "b.go", Status: "modified", Added: 2, Removed: 1},
			{Path: "a.go", Status: "added", Added: 8},
		},
		[]packet.FileNote{
			{Path: "a.go", Summary: "New helper."},
			{Path: "missing.go", Summary: "drop me"},
			{Path: "b.go", Summary: ""},
		},
	)
	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0].Path != "b.go" || rows[0].Summary != "" {
		t.Fatalf("empty note must not invent a summary: %+v", rows[0])
	}
	if rows[1].Path != "a.go" || rows[1].Summary != "New helper." {
		t.Fatalf("note must attach by path: %+v", rows[1])
	}
}
