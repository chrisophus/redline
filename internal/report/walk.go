package report

import (
	"github.com/ccason/redline/internal/change"
	"github.com/ccason/redline/internal/findings"
)

// fileWalkRow is one line of the change walkthrough.
type fileWalkRow struct {
	Path    string
	Status  string
	Added   int
	Removed int

	// Findings is how many findings landed on this file, and Severity the
	// worst of them. The walk is the first list a reviewer reads, so it has to
	// say which rows are worth opening — a flat list of changed files sends
	// them hunting through a collapsed section for the one file that matters.
	Findings int
	Severity string
}

// fileWalk lists every changed file in change order. A reviewer who has not
// read the diff still needs this list — especially when no pane examined the
// change.
func fileWalk(files []change.File, fs []findings.Finding) []fileWalkRow {
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
