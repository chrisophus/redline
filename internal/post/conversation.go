package post

import (
	"fmt"
	"strings"

	"github.com/chrisophus/redline/internal/conversation"
)

// maxThreadText bounds each side of one exchange. A reply is the agent's
// answer to one comment, so anything past this is better read on the report.
const maxThreadText = 6000

// WithConversation adds what the reviewer sent an agent from the report page,
// and what the agent replied, so a review done locally shows up on the pull
// request. Each exchange becomes a line comment when its line is on the new
// side of the diff, and otherwise goes in the review body, for the reason a
// finding does: one comment off the diff would make GitHub reject the whole
// review.
//
// The fingerprint covers the id and whether there is a reply yet, so an
// exchange posted before the agent answered is posted again, once, with the
// answer.
func (p Payload) WithConversation(items []conversation.Item, commentable map[string]map[int]bool) Payload {
	for _, it := range items {
		fp := threadFingerprint(it)
		if it.File != "" && it.Line > 0 && it.Side != "old" && lineCommentable(commentable, it.File, it.Line) {
			p.Comments = append(p.Comments, Comment{
				Path: it.File, Line: it.Line, Side: "RIGHT",
				Body:        threadBody(it) + "\n\n" + fpMarker(p.CommitID, fp),
				Fingerprint: fp,
			})
			continue
		}
		p.bodyThreads = append(p.bodyThreads, it)
	}
	p.Body = p.renderBody()
	return p
}

func threadFingerprint(it conversation.Item) string {
	state := "open"
	if strings.TrimSpace(it.Reply) != "" {
		state = "replied"
	}
	return "conversation:" + it.ID + ":" + state
}

// reviewerSaid is the reviewer's side of an exchange in words. An Explain or
// Apply sent with nothing typed still asked for something, so it says what.
func reviewerSaid(it conversation.Item) string {
	text := strings.TrimSpace(it.Text)
	switch it.Action {
	case "explain":
		if text == "" {
			return "Asked the agent to explain this."
		}
		return "Asked the agent to explain this: " + text
	case "apply":
		fix := strings.TrimSpace(it.Fix)
		switch {
		case text != "":
			return "Asked the agent to change this: " + text
		case fix != "":
			return "Asked the agent to apply the fix: " + fix
		}
		return "Asked the agent to change this."
	}
	return text
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxThreadText {
		return s[:maxThreadText] + "\n\n_(cut short; the rest is on the report)_"
	}
	return s
}

func quote(s string) string {
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}

func threadBody(it conversation.Item) string {
	var b strings.Builder
	b.WriteString("**Reviewer**")
	if it.On == "finding" && it.Rule != "" {
		fmt.Fprintf(&b, " on `%s`", it.Rule)
	}
	b.WriteString("\n\n")
	b.WriteString(quote(clip(reviewerSaid(it))))
	if reply := clip(it.Reply); reply != "" {
		b.WriteString("\n\n**Agent reply**\n\n")
		b.WriteString(reply)
	}
	return b.String()
}

// conversationSection is the exchanges that could not be anchored to a line.
// It is cut at budget, saying how many it left out, so it can never be what
// makes the body too long for GitHub.
func conversationSection(threads []conversation.Item, head string, budget int) string {
	if len(threads) == 0 {
		return ""
	}
	const heading = "### Review conversation\n\n"
	var b strings.Builder
	b.WriteString(heading)
	for i, it := range threads {
		var e strings.Builder
		if loc := it.File; loc != "" {
			if it.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, it.Line)
			}
			fmt.Fprintf(&e, "`%s`\n\n", loc)
		}
		e.WriteString(threadBody(it))
		e.WriteString("\n\n")
		e.WriteString(fpMarker(head, threadFingerprint(it)))
		e.WriteString("\n\n")
		if b.Len()+e.Len() > budget-200 {
			fmt.Fprintf(&b, "_%d more on the report._\n\n", len(threads)-i)
			break
		}
		b.WriteString(e.String())
	}
	if b.Len() == len(heading) {
		return ""
	}
	return b.String()
}
