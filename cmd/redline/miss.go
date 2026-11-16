package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/run"
)

// cmdMiss records a defect that a review of a pull request's head should have
// reported. It reads the session archived for that head and writes one line to
// the ledger, and it never calls a model or GitHub.
//
// The head is optional. A defect older than Redline has no session to read, and
// is still worth having in the ledger: the entry then says so with nulls
// instead of guessing.
func cmdMiss(o opts) error {
	e, err := missEntry(o)
	if err != nil {
		return err
	}
	root := o.out
	if root == "" {
		root = ".redline"
	}
	prDir := filepath.Join(root, sessionsDir, "pr-"+strconv.Itoa(e.PR))
	if o.head != "" {
		dir, full, rerr := findHead(prDir, o.head)
		switch {
		case rerr == nil:
			e.Head = full
			if err := miss.Examine(&e, dir); err != nil {
				return err
			}
		case errors.Is(rerr, run.ErrNoHead):
			fmt.Fprintf(os.Stderr, "redline: %v\nredline: recording the miss without a session, so nothing is worked out for it\n", rerr)
		default:
			return rerr
		}
	}
	ledger := o.jsonl
	if ledger == "" {
		ledger = root
	}
	if err := miss.Append(ledger, e); err != nil {
		return err
	}
	fmt.Println(describeMiss(e))
	return nil
}

// missEntry checks the flags and builds the entry they describe, before any
// session is read.
func missEntry(o opts) (miss.Entry, error) {
	var e miss.Entry
	name := o.targetSession("miss")
	n, err := strconv.Atoi(strings.TrimPrefix(name, "pr-"))
	if o.pr != "" && (err != nil || n <= 0) {
		return e, fmt.Errorf("--pr %q: give a pull request number or URL", o.pr)
	}
	switch {
	case o.defectFile == "":
		return e, errors.New("--file is required: the file the defect is in")
	case o.defectLine <= 0:
		return e, errors.New("--line is required: the line of the defect in the file at the reviewed head")
	case strings.TrimSpace(o.summary) == "":
		return e, errors.New("--summary is required: what the defect was")
	case o.fixPR < 0:
		return e, fmt.Errorf("--fix-pr %d: give a pull request number", o.fixPR)
	}
	if o.pr == "" {
		return e, errors.New("--pr is required: the pull request that introduced the defect")
	}
	if o.head != "" && !isHex(o.head) {
		return e, fmt.Errorf("--head %q: give a commit, or the start of one, in hex", o.head)
	}
	return miss.Entry{
		Recorded: time.Now().UTC(),
		PR:       n,
		Head:     strings.ToLower(o.head),
		File:     filepath.ToSlash(filepath.Clean(o.defectFile)),
		Line:     o.defectLine,
		Summary:  strings.TrimSpace(o.summary),
		FixPR:    o.fixPR,
	}, nil
}

func isHex(s string) bool {
	for _, r := range strings.ToLower(s) {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return s != ""
}

// findHead finds the session that reviewed head. An archived head is the
// answer. A pull request reviewed before heads were kept has only its latest
// session, and that serves when it is for the same commit.
func findHead(prDir, head string) (dir, full string, err error) {
	dir, full, err = run.ResolveHead(prDir, head)
	if err == nil || !errors.Is(err, run.ErrNoHead) {
		return dir, full, err
	}
	res, lerr := run.LoadSession(prDir)
	if lerr != nil || res.Change == nil || res.Change.Target == nil {
		return "", "", err
	}
	if sha := strings.ToLower(res.Change.Target.Head); sha != "" && strings.HasPrefix(sha, strings.ToLower(head)) {
		return prDir, sha, nil
	}
	return "", "", err
}

// describeMiss says what the entry found, in the words a person would use to
// answer whether the review could have caught it.
func describeMiss(e miss.Entry) string {
	where := fmt.Sprintf("%s:%d on PR %d", e.File, e.Line, e.PR)
	if e.LineInDiff == nil {
		return "redline: recorded a miss at " + where + ", with no session to check it against"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "redline: recorded a miss at %s, head %s. ", where, e.Head[:min(7, len(e.Head))])
	switch {
	case *e.LineInDiff:
		b.WriteString("The line was in the reviewed diff. ")
	case *e.FileInDiff:
		b.WriteString("The file was in the reviewed diff, but not that line. ")
	default:
		b.WriteString("The file was not in the reviewed diff. ")
	}
	switch {
	case *e.FindingTouched:
		b.WriteString("A finding touched it.")
	case *e.LookupTouched:
		b.WriteString("A lookup read it and no finding touched it.")
	default:
		b.WriteString("No finding or lookup touched it.")
	}
	return b.String()
}
