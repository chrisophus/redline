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
			n, err := strconv.Atoi(m[1])
			// A start below 1 names no real line in the new file (git only
			// emits one for a hunk that adds nothing, which carries no
			// commentable "+"/context lines anyway); treat it the same as
			// an unparseable header rather than anchor a comment at 0.
			newLine, inHunk = n, err == nil && n >= 1
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
		case strings.HasPrefix(line, " "):
			// A context line exists on both sides; commentable, then advance.
			// The prefix must be a literal space. A blank line in the source
			// still arrives as " ", so requiring it costs nothing and keeps a
			// record that is not a diff line from being counted as one.
			out[newLine] = true
			newLine++
		default:
			// Anything else is not a line of the new file: "\ No newline at end
			// of file", or the empty final record strings.Split leaves when a
			// patch ends in a newline. Counting that record as context marked a
			// line one past the end of the last hunk as commentable, which is
			// the out-of-diff anchor this file exists to prevent. GitHub does
			// not terminate the patch field with a newline today, so this was
			// unreachable through the files API, but CommentableLines is
			// exported and a caller with a newline-terminated patch would hit
			// it. Skipping without advancing can only under-report, which
			// demotes a finding to the body and is always safe.
		}
	}
	return out
}
