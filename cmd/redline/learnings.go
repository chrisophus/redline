package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
func cmdLearnings(o opts) error {
	if o.misses {
		return learnFromMisses(o)
	}
	res, err := run.LoadSession(o.out)
	if err != nil {
		return err
	}
	if len(res.PriorReview) == 0 {
		fmt.Fprintln(os.Stderr,
			"redline: this session carries no earlier review threads. "+
				"Run `redline run --pr N` on a pull request Redline has already reviewed.")
		return nil
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
	if drafted == 0 {
		fmt.Fprintln(os.Stderr,
			"redline: nothing on this pull request looks like a convention worth writing down. "+
				"A finding somebody explained in a reply is what this reads; one nobody "+
				"answered says nothing either way.")
		return nil
	}
	fmt.Println("# Draft review rules, from what people said about earlier findings.")
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
	fmt.Fprintf(os.Stderr, "redline: drafted %d rule(s) from %d thread(s)\n", drafted, len(res.PriorReview))
	return nil
}

// learnFromMisses drafts a rule for each recorded miss, in the shape the
// thread drafts take. A miss is a defect somebody fixed later and wrote down,
// which makes it a better source than a reply in one way and a worse one in
// another: it is a fact about the code and not an opinion, and it is one
// instance, so the draft says what happened and leaves the general rule to the
// person who reads it. --head is not read here, because the ledger already
// names the head each miss was recorded against.
func learnFromMisses(o opts) error {
	if o.head != "" {
		return fmt.Errorf("--head picks one head's session, and --misses reads the ledger, which names its own heads; drop one")
	}
	dir := o.jsonl
	if dir == "" {
		dir = o.out
	}
	entries, err := miss.Read(dir)
	if err != nil {
		return err
	}
	if o.pr != "" {
		var n int
		if m := prURLNumber.FindStringSubmatch(o.pr); m != nil {
			n, _ = strconv.Atoi(m[1])
		} else {
			n, _ = strconv.Atoi(strings.TrimPrefix(o.pr, "#"))
		}
		if n <= 0 {
			return fmt.Errorf("--pr %q: give a pull request number or URL", o.pr)
		}
		entries = slices.DeleteFunc(entries, func(e miss.Entry) bool { return e.PR != n })
	}
	if len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "redline: no misses recorded in %s. Add one with `redline miss`.\n",
			filepath.Join(dir, miss.File))
		return nil
	}
	fmt.Println("# Draft review rules, from defects a review should have caught.")
	fmt.Println("# Nothing is written for you. Each one is a single defect, so widen the")
	fmt.Println("# text into the convention it broke or cut it, keep the rest under")
	fmt.Println("# `review:` in .redline.yml, and commit it like any other code.")
	fmt.Println()
	fmt.Println("review:")
	fmt.Println("  instructions:")
	for _, e := range entries {
		fmt.Print(draftMiss(e))
	}
	fmt.Fprintf(os.Stderr, "redline: drafted %d rule(s) from %d miss(es)\n", len(entries), len(entries))
	return nil
}

func draftMiss(e miss.Entry) string {
	var b strings.Builder
	b.WriteString("    - scope: [" + scopeFor(feedback.Thread{File: e.File}) + "]\n")
	b.WriteString("      text: |\n")
	fmt.Fprintf(&b, "        A defect at %s:%d got past review of PR %d:\n", e.File, e.Line, e.PR)
	for _, line := range strings.Split(e.Summary, "\n") {
		fmt.Fprintf(&b, "        %s\n", strings.TrimSpace(line))
	}
	if e.LineInDiff != nil && !*e.LineInDiff {
		b.WriteString("        The line was not in the diff that was reviewed.\n")
	}
	if e.FixPR > 0 {
		fmt.Fprintf(&b, "      learned_from: \"PR %d, fixed in PR %d\"\n", e.PR, e.FixPR)
	} else {
		fmt.Fprintf(&b, "      learned_from: \"PR %d\"\n", e.PR)
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
