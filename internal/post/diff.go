package post

import (
	"regexp"
	"strconv"
	"strings"
)

// hunkHeader matches a unified-diff hunk header and captures the new-file start
// line, e.g. "@@ -12,7 +14,9 @@ func foo()" captures 14.
var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// CommentableLines turns the per-file patches GitHub returns for a pull request
// (the "patch" field of the list-files API) into the set of new-file line
// numbers a review comment may anchor to on each file.
//
// This is load-bearing for a safe post: GitHub rejects the entire review with a
// 422 if any one comment targets a line outside the diff, so a finding whose
// line is not commentable must be demoted to the review body instead. Added and
// context lines on the new side are commentable; removed lines are not, and a
// file with no patch (binary, or too large for GitHub to return) contributes no
// commentable lines, so its findings ride in the body.
func CommentableLines(patchByFile map[string]string) map[string]map[int]bool {
	out := make(map[string]map[int]bool, len(patchByFile))
	for file, patch := range patchByFile {
		lines := commentableInPatch(patch)
		if len(lines) > 0 {
			out[file] = lines
		}
	}
	return out
}

func commentableInPatch(patch string) map[int]bool {
	out := map[int]bool{}
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			newLine, _ = strconv.Atoi(m[1])
			inHunk = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			// An added line on the new side; commentable, then advance.
			out[newLine] = true
			newLine++
		case strings.HasPrefix(line, "-"):
			// Removed from the old side; the new-side counter does not move.
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file" — metadata, not a line.
		default:
			// A context line (leading space, or an empty trailing line) exists on
			// both sides; commentable, then advance.
			out[newLine] = true
			newLine++
		}
	}
	return out
}
