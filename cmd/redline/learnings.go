package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/chrisophus/redline/internal/feedback"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/run"
)

// cmdLearnings drafts review rules from the replies a pull request already
// made to Redline's own findings.
//
// It writes nothing. What it prints is a starting point a person edits and
// commits, which is the whole design: a loop that wrote its own rules from
// dismissals would learn to say less every time an author pushed back, and
// saying less is not the same as being right. The finding people most want to
// ignore is sometimes the one they most need.
//
// It reads the threads the last run already froze into the session rather than
// fetching them again. That keeps this command free, and it means the drafts
// describe the same conversation the review was shown.
//
// The ledger of misses (see miss.go) is the second source. A miss is a defect a
// review let through, recorded by a person, so a rule drawn from it says what
// went wrong and where and leaves the cure to whoever edits the draft.
func cmdLearnings(o opts) error {
	dir := o.jsonl
	if dir == "" {
		dir = o.ledgerDir()
	}
	misses, err := miss.Read(dir)
	if err != nil {
		return err
	}
	res, err := run.LoadSession(o.out)
	if err != nil {
		// Misses stand on their own: they are not about the session in front of
		// the command, and a repository with a ledger and no session still has
		// something to learn from.
		if len(misses) == 0 {
			return err
		}
		res = &run.Result{}
	}
	var drafted int
	var b strings.Builder
	for _, t := range res.PriorReview {
		if !worthLearning(t) {
			continue
		}
		drafted++
		b.WriteString(draft(t, res))
	}
	for _, m := range misses {
		drafted++
		b.WriteString(draftMiss(m))
	}
	if drafted == 0 {
		if len(res.PriorReview) == 0 {
			fmt.Fprintln(os.Stderr,
				"redline: this session carries no earlier review threads and no misses are recorded. "+
					"Run `redline run --pr N` on a pull request Redline has already reviewed, "+
					"or record one with `redline miss`.")
			return nil
		}
		fmt.Fprintln(os.Stderr,
			"redline: nothing on this pull request looks like a convention worth writing down. "+
				"A finding somebody explained in a reply is what this reads; one nobody "+
				"answered says nothing either way.")
		return nil
	}
	fmt.Println("# Draft review rules, from what people said about earlier findings")
	fmt.Println("# and from defects a review let through.")
	fmt.Println("# Nothing is written for you. Read each one, cut what is wrong, keep the")
	fmt.Println("# rest under `review:` in .redline.yml, and commit it like any other code.")
	fmt.Println("#")
	fmt.Println("# A reply is somebody explaining their own change, so a rule drawn from one")
	fmt.Println("# can be wrong. That is why a person writes it down and why a learned rule")
	fmt.Println("# never outranks a rule the team wrote in a file.")
	fmt.Println()
	fmt.Println("review:")
	fmt.Println("  instructions:")
	fmt.Print(b.String())
	fmt.Fprintf(os.Stderr, "redline: drafted %d rule(s) from %d thread(s) and %d miss(es)\n",
		drafted, len(res.PriorReview), len(misses))
	return nil
}

// draftMiss renders one miss as a rule somebody can edit. The scope is the
// file's directory, for the reason draft gives. The text says whether the
// review had the line in front of it, because a defect in code the review was
// shown and a defect in code it never saw call for different rules.
func draftMiss(m miss.Entry) string {
	t := feedback.Thread{File: m.File, Line: m.Line}
	var b strings.Builder
	b.WriteString("    - scope: [" + scopeFor(t) + "]\n")
	b.WriteString("      text: |\n")
	fmt.Fprintf(&b, "        A review of PR #%d let through a defect at %s:\n", m.PR, location(t))
	for _, line := range strings.Split(strings.TrimSpace(m.Summary), "\n") {
		fmt.Fprintf(&b, "        %s\n", strings.TrimSpace(line))
	}
	var kept, dropped bool
	for _, f := range m.Findings {
		switch f.By {
		case "finding":
			kept = true
		case "proposed":
			dropped = true
		}
	}
	// A proposed finding with no kept one on the line means the reviewer found
	// it and the ruling dropped it, the miss most worth a rule.
	switch {
	case m.LineInDiff != nil && *m.LineInDiff && len(m.Findings) == 0:
		b.WriteString("        The line was in the reviewed diff and no finding was made on it.\n")
	case m.LineInDiff != nil && *m.LineInDiff && !kept && dropped:
		b.WriteString("        The line was in the reviewed diff and the reviewer proposed a finding on it, but the ruling dropped it.\n")
	case m.LineInDiff != nil && *m.LineInDiff:
		b.WriteString("        The line was in the reviewed diff and a finding sat on it that did not stop the defect.\n")
	case m.LineInDiff != nil && m.FileInDiff != nil && *m.FileInDiff:
		b.WriteString("        The file was in the reviewed diff, but the change did not add this line, so the defect sits in code the change only touched around.\n")
	case m.LineInDiff != nil:
		b.WriteString("        The line was not in the reviewed diff, so it was reached through code the diff did not touch.\n")
	}
	if m.FixPR > 0 {
		fmt.Fprintf(&b, "      learned_from: \"fixed in PR #%d\"\n", m.FixPR)
	} else {
		b.WriteString("      learned_from: \"\"  # paste the link to the fix\n")
	}
	b.WriteString("\n")
	return b.String()
}

// worthLearning reports whether a thread taught anything. A reply is the
// signal: somebody took the time to explain, and the explanation is the rule.
// A thread nobody answered says nothing either way, and a thumbs-down with no
// words says only that one person disagreed.
func worthLearning(t feedback.Thread) bool {
	for _, r := range t.Replies {
		if len(strings.Fields(r.Body)) >= 4 {
			return true
		}
	}
	return false
}

// draft renders one thread as a rule somebody can edit. The scope is the
// changed file's directory, which is the narrowest honest guess: the reply was
// about that code, and widening it to the repository would be inventing a
// claim nobody made.
func draft(t feedback.Thread, res *run.Result) string {
	var b strings.Builder
	b.WriteString("    - scope: [" + scopeFor(t) + "]\n")
	b.WriteString("      text: |\n")
	for _, r := range t.Replies {
		who := r.Author
		if who == "" {
			who = "someone"
		}
		fmt.Fprintf(&b, "        %s, on a finding about %s:\n", who, location(t))
		for _, line := range strings.Split(strings.TrimSpace(r.Body), "\n") {
			fmt.Fprintf(&b, "        %s\n", strings.TrimSpace(line))
		}
	}
	if url := prURL(res); url != "" {
		fmt.Fprintf(&b, "      learned_from: %q\n", url)
	} else {
		b.WriteString("      learned_from: \"\"  # paste the thread's link\n")
	}
	b.WriteString("\n")
	return b.String()
}

func scopeFor(t feedback.Thread) string {
	if t.File == "" {
		return ""
	}
	if i := strings.LastIndex(t.File, "/"); i > 0 {
		return fmt.Sprintf("%q", t.File[:i]+"/**")
	}
	return fmt.Sprintf("%q", t.File)
}

func location(t feedback.Thread) string {
	if t.File == "" {
		return "this change"
	}
	if t.Line > 0 {
		return fmt.Sprintf("%s:%d", t.File, t.Line)
	}
	return t.File
}

func prURL(res *run.Result) string {
	if res == nil || res.Target == nil || res.Target.PR == nil {
		return ""
	}
	return res.Target.PR.URL
}
