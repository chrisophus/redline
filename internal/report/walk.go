package report

import "github.com/ccason/redline/internal/packet"

// fileWalkRow is one line of the change walkthrough: the path the packet
// already knows, plus the agent's sentence when they supplied one.
type fileWalkRow struct {
	Path    string
	Status  string
	Added   int
	Removed int
	Summary string
}

// fileWalk lists every changed file in packet order. Agent notes attach by
// path; notes for paths the packet does not contain are dropped. A reviewer
// who has not read the diff still needs this list — especially when no pane
// examined the change.
func fileWalk(files []packet.FileChange, notes []packet.FileNote) []fileWalkRow {
	byPath := map[string]string{}
	for _, n := range notes {
		if n.Path == "" || n.Summary == "" {
			continue
		}
		byPath[n.Path] = n.Summary
	}
	out := make([]fileWalkRow, 0, len(files))
	for _, f := range files {
		out = append(out, fileWalkRow{
			Path:    f.Path,
			Status:  f.Status,
			Added:   f.Added,
			Removed: f.Removed,
			Summary: byPath[f.Path],
		})
	}
	return out
}
