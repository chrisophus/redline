package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/run"
)

// cmdMiss records a defect that a review should have caught.
//
// The facts about the review come from the session kept for the head, not from
// asking anyone: whether the line was added by the reviewed diff, and whether
// a finding or a lookup went near it. A head with no kept session, or no head
// at all, still records the miss, with those answers left unset. They are
// unknown there, and writing false would say the review had been checked.
func cmdMiss(o opts) error {
	pr, err := missPR(o.pr)
	if err != nil {
		return err
	}
	switch {
	case o.missFile == "":
		return errors.New("--file is required: the file the defect is in")
	case o.missLine <= 0:
		return errors.New("--line is required: the line of the defect, above zero")
	case strings.TrimSpace(o.missSummary) == "":
		return errors.New("--summary is required: what the defect was")
	}
	e := miss.Entry{
		Recorded: time.Now().UTC(), PR: pr,
		File: filepath.ToSlash(o.missFile), Line: o.missLine,
		Summary: strings.TrimSpace(o.missSummary), FixPR: o.missFixPR,
	}
	if o.head != "" {
		// Head is set by examineHead, from the session it found. A --head that
		// names no kept session is not written, so the ledger never holds a
		// head that is not a commit.
		if err := examineHead(&e, filepath.Join(o.out, sessionsDir, "pr-"+strconv.Itoa(pr)), o.head); err != nil {
			fmt.Fprintf(os.Stderr, "redline: %v; recording the miss without what the review showed\n", err)
		}
	}
	dir := o.jsonl
	if dir == "" {
		dir = o.out
	}
	if err := miss.Append(dir, e); err != nil {
		return err
	}
	fmt.Printf("redline: recorded a miss on PR #%d at %s:%d in %s\n", pr, e.File, e.Line, filepath.Join(dir, miss.File))
	fmt.Println(describeMiss(e))
	return nil
}

// examineHead reads the session kept for head under the pull request's session
// directory into e.
func examineHead(e *miss.Entry, session, head string) error {
	if !headArgRE.MatchString(head) {
		return fmt.Errorf("--head %q: give a commit SHA, at least 7 hex characters", head)
	}
	dir, err := findHead(session, head)
	if err != nil {
		return err
	}
	res, err := run.LoadSession(dir)
	if err != nil {
		return err
	}
	// A review that never ran leaves no trace, and the findings and diff are
	// still worth reading. A trace outlives the run that wrote it, so one that
	// names another change than the session's is from an earlier head and is
	// left out.
	tr, err := postmortem.Load(dir)
	if err != nil || tr == nil || tr.Revision != change.ReviewIdentity(res.Report.BaseSHA, res.Change) {
		tr = nil
	}
	miss.Examine(e, miss.Session{Change: res.Change, Findings: res.Report.Findings, Trace: tr})
	e.Head, e.Session = filepath.Base(dir), dir
	return nil
}

// describeMiss is the one line the command prints about what it found.
func describeMiss(e miss.Entry) string {
	if e.Session == "" {
		return "redline: no kept session for this miss, so nothing is said about the review"
	}
	where := "not in the reviewed diff"
	switch {
	case e.LineInDiff != nil && *e.LineInDiff:
		where = "in the reviewed diff"
	case e.FileInDiff != nil && *e.FileInDiff:
		where = "in a file the review saw, on a line the change did not add"
	}
	return fmt.Sprintf("redline: the line was %s; %d finding(s) and %d lookup(s) covered it", where, len(e.Findings), len(e.Lookups))
}

// missPR reads --pr as a number, "#12" or a pull request URL.
func missPR(s string) (int, error) {
	if s == "" {
		return 0, errors.New("--pr is required: the pull request whose review missed it")
	}
	if m := prURLNumber.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--pr %q: give a pull request number", s)
	}
	return n, nil
}
