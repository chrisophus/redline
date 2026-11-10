package dispositions

import (
	"fmt"
	"strings"
	"time"
)

type fixResult struct {
	sha     string
	time    time.Time
	mapped  bool // the finding's commit was found in the PR's commit list
	deleted bool
}

// findFix walks the PR's commits after the finding's head and reports the first non-merge commit that changed
// the finding's anchor, carrying the anchor window forward through each commit's diff so earlier edits that
// shift line numbers do not read as a fix or hide one.
func findFix(pr PR, file string, line, startLine int, head string, diff Differ) (fixResult, error) {
	idx := -1
	for i, c := range pr.Commits {
		if head != "" && strings.EqualFold(c.SHA, head) {
			idx = i
			break
		}
	}
	if idx < 0 || file == "" {
		return fixResult{}, nil
	}
	res := fixResult{mapped: true}
	win := AnchorWindow(line, startLine, AnchorRadius)
	for _, c := range pr.Commits[idx+1:] {
		hunks, deleted, err := diff.Hunks(c.SHA, file)
		if err != nil {
			return res, fmt.Errorf("diff %s %s: %w", short(c.SHA), file, err)
		}
		if deleted {
			res.deleted = true
			return res, nil
		}
		if len(hunks) == 0 {
			continue
		}
		if !c.IsMerge {
			if line == 0 || win.Overlaps(hunks) {
				res.sha, res.time = c.SHA, c.Time
				return res, nil
			}
		}
		if line > 0 {
			win = win.Forward(hunks)
		}
	}
	return res, nil
}

// shortSHALen is how much of a SHA error messages show.
const shortSHALen = 9

func short(s string) string {
	if len(s) > shortSHALen {
		return s[:shortSHALen]
	}
	return s
}
