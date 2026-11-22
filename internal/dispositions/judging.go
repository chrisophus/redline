package dispositions

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisophus/redline/internal/jev"
)

// Judging is what --jev adds to a run: Jev's reading of the author's replies, and its check that a commit
// a reply names fixes the finding. A nil *Judging does nothing, so Derive calls it without checking.
//
// Once a request to TypeSafe fails, no more are sent and the run carries on with the rules it had before.
// Notes then holds one line saying so.
type Judging struct {
	Ctx     context.Context
	Replies interface {
		Read(context.Context, jev.ReplyCase) (jev.ReplyScores, error)
	}
	Fixes interface {
		Fixed(context.Context, jev.FixCase) (float64, error)
	}
	// Code reads the repository. A failure to read it skips the fix check for that finding.
	Code Source

	down  bool
	notes []string
}

// Source is the part of git that --jev needs.
type Source interface {
	// FileAt returns a file's content at a commit.
	FileAt(commit, path string) (string, error)
	// CommitFiles returns what a commit changed against its first parent, one entry per file, in diff order.
	CommitFiles(commit string) ([]FileDiff, error)
}

// FileDiff is one file's part of a commit's diff, headers included.
type FileDiff struct {
	Path string
	Text string
}

// Notes are the things a reader of the summary should know about how Jev was used.
func (j *Judging) Notes() []string {
	if j == nil {
		return nil
	}
	return j.notes
}

func (j *Judging) fail(err error) {
	j.down = true
	j.notes = append(j.notes, fmt.Sprintf("TypeSafe could not be reached, so replies were read without --jev from here on: %v", err))
}

var htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// findingText is the first comment of a thread without the hidden markers.
func findingText(th *Thread) string {
	return strings.TrimSpace(htmlCommentRe.ReplaceAllString(th.Comments[0].Body, ""))
}

// authorReplies joins what the PR author said in the thread, oldest first.
func authorReplies(th *Thread) string {
	var parts []string
	for _, c := range th.Comments[1:] {
		if c.ByPRAuthor {
			parts = append(parts, strings.TrimSpace(c.Body))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// readReply asks Jev how the author's replies read. It returns nil when there is no Judging, no reply, or no
// answer.
func (j *Judging) readReply(th *Thread, file string) *jev.ReplyScores {
	if j == nil || j.down || j.Replies == nil {
		return nil
	}
	reply := authorReplies(th)
	if reply == "" {
		return nil
	}
	s, err := j.Replies.Read(j.Ctx, jev.ReplyCase{File: file, Finding: findingText(th), Reply: reply})
	if err != nil {
		j.fail(err)
		return nil
	}
	return &s
}

// declineReason says why a Not fixing reply declined, from Jev's answers. Wrong needs the "changed" answer
// to be low, because the wrong question also fires on a fix reply that explains the bug.
func declineReason(s jev.ReplyScores) string {
	switch {
	case disputed(s):
		return DeclineWrong
	case s.WontFix >= jev.Confident:
		return DeclineWontFix
	}
	return DeclineUnsure
}

func disputed(s jev.ReplyScores) bool { return s.Wrong >= jev.Confident && s.Changed < jev.Doubtful }

// decideFromScores sets the disposition of a thread whose reply has no marker line, and reports whether the
// answers were clear enough to. When they were not, the caller falls through to the anchor check.
func decideFromScores(drow *DispositionRow, s jev.ReplyScores, fix fixResult) bool {
	switch {
	case s.Changed >= jev.Confident:
		drow.Disposition, drow.Basis = string(DispFixed), BasisJev
		drow.FixCommitSha = fix.sha
	case disputed(s):
		drow.Disposition, drow.Basis, drow.DeclineReason = string(DispDeclined), BasisJev, DeclineWrong
	case s.WontFix >= jev.Confident:
		drow.Disposition, drow.Basis, drow.DeclineReason = string(DispDeclined), BasisJev, DeclineWontFix
	case s.Open >= jev.Confident:
		drow.Disposition, drow.Basis = string(DispOpen), BasisJev
	default:
		return false
	}
	return true
}

// codeRadius is how many lines either side of the finding's line the fix question is shown.
const codeRadius = 12

// changeLimit is the most of a commit's diff the fix question is shown. A smaller diff goes in whole.
const changeLimit = 9 * 1024

// verifyFix asks whether the commit a reply named fixes the finding, and records the answer on drow.
func (j *Judging) verifyFix(drow *DispositionRow, th *Thread, line int) {
	if j == nil || j.down || j.Fixes == nil || j.Code == nil || drow.FixCommitSha == "" || drow.FirstHeadSha == "" {
		return
	}
	src, err := j.Code.FileAt(drow.FirstHeadSha, drow.File)
	if err != nil {
		return
	}
	files, err := j.Code.CommitFiles(drow.FixCommitSha)
	if err != nil || len(files) == 0 {
		return
	}
	finding, reply := findingText(th), authorReplies(th)
	p, err := j.Fixes.Fixed(j.Ctx, jev.FixCase{
		File: drow.File, Finding: finding, CodeBefore: lines(src, line, codeRadius),
		Change: pickChange(files, drow.File, finding+"\n"+reply, changeLimit),
	})
	if err != nil {
		j.fail(err)
		return
	}
	drow.FixScore = &p
	switch {
	case p >= jev.Confident:
		drow.FixVerified = FixYes
	case p <= jev.Doubtful:
		drow.FixVerified = FixNo
	default:
		drow.FixVerified = FixUnsure
	}
}

// lines returns line-radius to line+radius of src, or nothing when the finding has no line.
func lines(src string, line, radius int) string {
	if line <= 0 {
		return ""
	}
	all := strings.Split(src, "\n")
	from, to := max(line-radius, 1), min(line+radius, len(all))
	if from > to {
		return ""
	}
	return strings.Join(all[from-1:to], "\n")
}

// pickChange is the diff the fix question sees. A commit under limit goes in whole. A larger one is cut to
// limit with the finding's own file first, then the files the finding or reply names, then the rest. Showing
// only the anchored file's diff made two fixes look wrong in the 2026-11-22 test, because the fix was in
// another file.
func pickChange(files []FileDiff, anchor, mentions string, limit int) string {
	total := 0
	for _, f := range files {
		total += len(f.Text)
	}
	if total <= limit {
		var b strings.Builder
		for _, f := range files {
			b.WriteString(f.Text)
		}
		return b.String()
	}
	rank := func(f FileDiff) int {
		switch {
		case f.Path == anchor:
			return 0
		case strings.Contains(mentions, f.Path) || strings.Contains(mentions, baseName(f.Path)):
			return 1
		}
		return 2
	}
	var b strings.Builder
	for r := range 3 {
		for _, f := range files {
			if rank(f) != r || b.Len() >= limit {
				continue
			}
			text := f.Text
			if left := limit - b.Len(); len(text) > left {
				text = strings.ToValidUTF8(text[:left], "")
			}
			b.WriteString(text)
		}
	}
	return b.String()
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
