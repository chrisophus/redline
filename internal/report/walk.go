package report

import (
	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/findings"
)

// fileWalkRow is one line of the change walkthrough.
type fileWalkRow struct {
	Path    string
	Status  string
	Added   int
	Removed int

	// Findings is how many findings landed on this file, and Severity the
	// worst of them.
	Findings int
	Severity string
}

// fileWalk lists every changed file in change order. The markdown report uses
// it for its file section; a reviewer who has not read the diff still needs the
// list, especially when no pane examined the change.
func fileWalk(files []change.File, fs []findings.Finding) []fileWalkRow {
	count, worst := fileFindingCounts(fs)
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

// fileFindingCounts returns, per file, how many findings landed on it and the
// worst severity among them. The HTML drill-in list uses this to mark which
// rows are worth opening: a flat list of changed files would send a reviewer
// hunting for the one file that carries a defect.
func fileFindingCounts(fs []findings.Finding) (count map[string]int, worst map[string]findings.Severity) {
	count = map[string]int{}
	worst = map[string]findings.Severity{}
	for _, f := range fs {
		if f.File == "" {
			continue
		}
		count[f.File]++
		if rank(f.Severity) > rank(worst[f.File]) {
			worst[f.File] = f.Severity
		}
	}
	return count, worst
}

// rank orders severities so the list can show the worst one per file.
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
