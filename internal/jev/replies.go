package jev

import (
	"context"
	"fmt"
)

// Thresholds for reading a Jev answer as yes or no.
//
// A test on 2026-11-22 asked Jev these questions about 138 Redline threads
// from five repositories, 98 of which had replies, for $0.017. At 0.7 or
// higher, the fix question confirmed 73 of 80 named fix commits, and the five
// it was unsure about were all partial fixes. The reply question for "the
// claim is wrong" also fired on fix replies that explain the bug ("Fixed in
// d6f8d14: the message now..."), so a dispute counts only when the "changed"
// answer is under Doubtful. Across the 98 answered findings, 8 were disputed
// as wrong (8%).
const (
	Confident = 0.7
	Doubtful  = 0.3
)

// ReplyCase is a finding and what the pull request's author said about it.
// Reply holds every reply by the author, oldest first, so that a correction to
// an earlier reply is seen.
type ReplyCase struct {
	File    string `json:"file"`
	Finding string `json:"finding"`
	Reply   string `json:"reply"`
}

// ReplyScores is the probability of yes for each of the four reply questions.
type ReplyScores struct {
	Changed, Wrong, WontFix, Open float64
}

// FixCase is a finding, the code it was about at the reviewed commit, and the
// change a reply named as the fix.
type FixCase struct {
	File       string `json:"file"`
	Finding    string `json:"finding"`
	CodeBefore string `json:"code_before"`
	Change     string `json:"change"`
}

const (
	changedAsk  = "Does the reply say the code was changed because of the finding?"
	changedTrue = "The reply says the author changed, fixed or removed the code the finding is about."
	changedNot  = "The reply says nothing was changed, that the finding is wrong, or that the problem remains."

	wrongAsk  = "Does the reply say the finding's claim is wrong: that the problem does not exist or cannot happen?"
	wrongTrue = "The reply says the described problem is not real, cannot occur, or has a different cause."
	wrongNot  = "The reply accepts that the problem is real, or does not argue with the claim."

	wontFixAsk  = "Does the reply say the problem is real but is deliberately not being changed?"
	wontFixTrue = "The reply accepts the problem exists and says it will stay as it is, for a reason."
	wontFixNot  = "The reply says the problem was fixed, says it is not a problem, or says nothing about leaving it."

	openAsk  = "Does the reply say the problem is still there?"
	openTrue = "The reply says the problem remains, is not yet handled, or is still open."
	openNot  = "The reply says the problem was fixed, is not a problem, or will not be changed."

	fixAsk  = "Does `change` fix the problem `finding` describes in `code_before`?"
	fixTrue = "The change alters the code so that the problem the finding names no longer happens, in this file or another."
	fixNot  = "The change leaves the problem in place, or touches only unrelated code or documentation."

	replyChars   = 6000
	findingChars = 6000
)

// Replies asks how an author's replies to a finding read.
type Replies struct {
	Client *Client
	meter
}

// Read asks the four reply questions in one request and returns each averaged
// over the repeated askings.
func (j *Replies) Read(ctx context.Context, c ReplyCase) (ReplyScores, error) {
	c.Finding, c.Reply = clipRunes(c.Finding, findingChars), clipRunes(c.Reply, replyChars)
	asks := []struct {
		name string
		q    Noul
	}{
		{"changed", Noul{Instructions: changedAsk, True: changedTrue, False: changedNot}},
		{"wrong", Noul{Instructions: wrongAsk, True: wrongTrue, False: wrongNot}},
		{"wontfix", Noul{Instructions: wontFixAsk, True: wontFixTrue, False: wontFixNot}},
		{"open", Noul{Instructions: openAsk, True: openTrue, False: openNot}},
	}
	qs := make(map[string]Noul, len(asks)*copies)
	for _, a := range asks {
		for n := range copies {
			qs[fmt.Sprintf("%s.%d", a.name, n)] = a.q
		}
	}
	got, usage, err := j.Client.Ask(ctx, c, qs)
	j.add(usage)
	if err != nil {
		return ReplyScores{}, fmt.Errorf("asking Jev how a reply reads: %w", err)
	}
	if len(got) != len(qs) {
		return ReplyScores{}, fmt.Errorf("jev answered %d of %d questions about a reply", len(got), len(qs))
	}
	mean := func(name string) float64 {
		sum := 0.0
		for n := range copies {
			sum += got[fmt.Sprintf("%s.%d", name, n)]
		}
		return sum / copies
	}
	return ReplyScores{Changed: mean("changed"), Wrong: mean("wrong"), WontFix: mean("wontfix"), Open: mean("open")}, nil
}

// Fixes asks whether a named commit fixes a finding.
type Fixes struct {
	Client *Client
	meter
}

// Fixed returns the probability that the change fixes the finding.
func (j *Fixes) Fixed(ctx context.Context, c FixCase) (float64, error) {
	c.Finding = clipRunes(c.Finding, findingChars)
	qs := make(map[string]Noul, copies)
	for n := range copies {
		qs[fmt.Sprintf("q%d", n)] = Noul{Instructions: fixAsk, True: fixTrue, False: fixNot}
	}
	got, usage, err := j.Client.Ask(ctx, c, qs)
	j.add(usage)
	if err != nil {
		return 0, fmt.Errorf("asking Jev whether a commit fixes a finding: %w", err)
	}
	if len(got) != len(qs) {
		return 0, fmt.Errorf("jev answered %d of %d questions about a fix", len(got), len(qs))
	}
	sum := 0.0
	for _, p := range got {
		sum += p
	}
	return sum / copies, nil
}
