package envelope

import (
	"fmt"
	"strings"
)

// dedupeCommits rewrites history content so each commit's message is carried
// once for the whole change.
//
// git log -L is asked per line range, so a commit that touched many ranges
// emits its whole message once per range. On this repository's own PR #83
// that was 98 history and removal expansions carrying 186 commit blocks
// between 18 distinct commits: one commit's message appeared 58 times, and
// the two roles together came to 429,000 tokens, 73% of everything resolved,
// against a context ceiling of 184,000. Twenty-eight history expansions were
// dropped for want of room while the room was full of the same essay.
//
// Seen, the only other deduplication here, cannot reach this. It removes
// expansion lines the diff already shows, and carriesHistory exempts these
// two roles from it on purpose: their content is commit messages rather than
// the source at the lines they name. That exemption is right and it left the
// one role with tenfold internal duplication with no deduplication at all.
//
// What is kept per expansion is the part that differs: the commit header and
// the hunk git printed for that range. Only the message body is replaced, by
// a line saying where to read it. Order is the ranked order, so the copy that
// survives in full is the one in the highest-ranked expansion, which is the
// one most likely to be kept when the budget binds.
func dedupeCommits(ranked []Expansion) {
	// where records the expansion that carried each commit's message in full.
	where := map[string]string{}
	for i := range ranked {
		if !ranked[i].Role.carriesHistory() {
			continue
		}
		ranked[i].Content = dedupeCommitsIn(ranked[i].Content, at(ranked[i]), where)
	}
}

// at names an expansion the way the reference line points at it.
func at(x Expansion) string {
	if x.StartLine <= 0 {
		return x.File
	}
	if x.EndLine > x.StartLine {
		return fmt.Sprintf("%s:%d-%d", x.File, x.StartLine, x.EndLine)
	}
	return fmt.Sprintf("%s:%d", x.File, x.StartLine)
}

// dedupeCommitsIn rewrites one expansion's content. A commit whose message is
// already carried elsewhere keeps its header and its hunk and loses the body.
func dedupeCommitsIn(content, here string, where map[string]string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		sha, ok := commitSHA(line)
		if !ok {
			b.WriteString(line)
			if i < len(lines)-1 {
				b.WriteString("\n")
			}
			continue
		}
		// The header runs to the blank line after Date:, then the message is
		// the indented block, then whatever git printed for the range.
		b.WriteString(line + "\n")
		i++
		for ; i < len(lines) && strings.TrimSpace(lines[i]) != ""; i++ {
			b.WriteString(lines[i] + "\n")
		}
		if i < len(lines) {
			b.WriteString(lines[i] + "\n") // the blank line
			i++
		}
		start := i
		for ; i < len(lines) && isMessageLine(lines[i]); i++ {
		}
		body := lines[start:i]
		i-- // the loop's own i++ steps onto the first line past the message
		if prior, seen := where[sha]; seen {
			fmt.Fprintf(&b, "    (message above, under %s)\n", prior)
			continue
		}
		where[sha] = here
		for _, m := range body {
			b.WriteString(m + "\n")
		}
	}
	return b.String()
}

// commitSHA reads the sha off a "commit <40 hex>" line.
func commitSHA(line string) (string, bool) {
	const prefix = "commit "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	sha := strings.TrimSpace(line[len(prefix):])
	if len(sha) < 7 {
		return "", false
	}
	for _, r := range sha {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", false
		}
	}
	return sha, true
}

// isMessageLine reports whether a line belongs to a commit message body. git
// indents the message by four spaces; a blank line inside it is still part of
// it, and anything flush left has ended it.
func isMessageLine(line string) bool {
	return strings.TrimSpace(line) == "" || strings.HasPrefix(line, "    ")
}
