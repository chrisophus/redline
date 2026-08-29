package report

import (
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
)

// fileWalkRow is one line of the change walkthrough: the path the packet
// already knows, plus the agent's sentence when they supplied one.
type fileWalkRow struct {
	Path    string
	Status  string
	Added   int
	Removed int
	Summary string

	// Findings is how many findings landed on this file, and Severity the
	// worst of them. The walk is the first list a reviewer reads, so it has to
	// say which rows are worth opening — a flat list of changed files sends
	// them hunting through a collapsed section for the one file that matters.
	Findings int
	Severity string
}

// fileWalk lists every changed file in packet order. Agent notes attach by
// path; notes for paths the packet does not contain are dropped. A reviewer
// who has not read the diff still needs this list — especially when no pane
// examined the change.
func fileWalk(files []packet.FileChange, notes []packet.FileNote, fs []findings.Finding) []fileWalkRow {
	byPath := map[string]string{}
	for _, n := range notes {
		if n.Path == "" || n.Summary == "" {
			continue
		}
		byPath[n.Path] = n.Summary
	}
	count := map[string]int{}
	worst := map[string]findings.Severity{}
	for _, f := range fs {
		if f.File == "" {
			continue
		}
		count[f.File]++
		if rank(f.Severity) > rank(worst[f.File]) {
			worst[f.File] = f.Severity
		}
	}

	out := make([]fileWalkRow, 0, len(files))
	for _, f := range files {
		out = append(out, fileWalkRow{
			Path:     f.Path,
			Status:   f.Status,
			Added:    f.Added,
			Removed:  f.Removed,
			Summary:  byPath[f.Path],
			Findings: count[f.Path],
			Severity: string(worst[f.Path]),
		})
	}
	return out
}

// rank orders severities so the walk can show the worst one per file.
func rank(s findings.Severity) int {
	switch s {
	case findings.SeverityError:
		return 3
	case findings.SeverityWarning:
		return 2
	case findings.SeverityInfo:
		return 1
	}
	return 0
}
