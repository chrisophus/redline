// Package postmortem keeps what a review did, so someone can read it back.
//
// `redline review` runs three things in a row: a reviewer that proposes
// findings, a scout that looks up what each finding said would settle it, and
// a ruling that decides which findings reach the author. What survives on
// disk is the last of those. review.json holds the findings that were kept
// and a verdict on the ones that were not, and nothing holds what the
// reviewer originally proposed, which questions went to the scout, what the
// scout looked at, or what it came back with.
//
// That is the wrong thing to lose. The failure people actually hit is a
// review that comes back with two findings out of nine, and the question it
// raises is not answerable from review.json: were the other seven wrong, or
// did the lookups fail to find the code that would have confirmed them? Those
// are opposite problems. One says the reviewer is noisy and the other says
// the scout is looking in the wrong place, and the fix for each makes the
// other worse.
//
// So `review` writes this file beside the report and `redline postmortem`
// renders it. It is written on every review rather than under a flag: the
// interesting run is always the one that already happened, and a debug switch
// you have to have set in advance is a switch you set after the run you
// needed it for.
package postmortem

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
	"github.com/chrisophus/redline/internal/review"
)

// File is where the trace lives, beside findings.json and review.json in the
// evidence directory.
const File = "postmortem.json"

// SchemaVersion is bumped when a reader would misread an older file. Nothing
// but `redline postmortem` reads it, and an unreadable trace is never fatal,
// so this is here to say which shape a file is rather than to gate anything.
const SchemaVersion = 1

// Trace is one review, from what the reviewer proposed to what the ruling
// decided.
type Trace struct {
	SchemaVersion int       `json:"schemaVersion"`
	Wrote         time.Time `json:"wrote"`
	// Target is the change this was a review of, as the run describes it, and
	// Revision the identity `run` stamps a review with. A trace outlives the
	// session it came from, the same way review.json does, so it has to be
	// able to say which change it is about.
	Target   string `json:"target,omitempty"`
	Revision string `json:"revision,omitempty"`

	API    string `json:"api,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Samples is how many independent reviews were unioned into the findings
	// below. Worth reading beside a finding nobody else found.
	Samples int `json:"samples,omitempty"`
	// Verified says the checking pass ran, and VerifyFailed why it produced
	// nothing when it did. A review nobody checked and a review whose check
	// broke are both unchecked, and only one of them looks like it worked.
	Verified     bool   `json:"verified,omitempty"`
	VerifyFailed string `json:"verifyFailed,omitempty"`

	CostUSD   float64 `json:"costUSD,omitempty"`
	CostKnown bool    `json:"costKnown,omitempty"`

	// Findings are stage one's, in the order it wrote them, each with the
	// question it named and the ruling it got.
	Findings []Finding `json:"findings,omitempty"`
	// Lookup is what the scout did about them.
	Lookup Lookup `json:"lookup,omitzero"`
}

// Finding is one proposed finding and what happened to it.
type Finding struct {
	// ID is the short id the later stages address it by, c1, c2 and so on.
	// It is also what a record says it answers, which is what connects a
	// finding to the code the scout fetched for it.
	ID         string              `json:"id"`
	File       string              `json:"file,omitempty"`
	Line       int                 `json:"line,omitempty"`
	Severity   findings.Severity   `json:"severity,omitempty"`
	Confidence findings.Confidence `json:"confidence,omitempty"`
	// Body is the finding as the reviewer wrote it, before the ruling folded
	// a withdrawn one down.
	Body     string            `json:"body,omitempty"`
	Question findings.Question `json:"question,omitempty"`
	// Asked says this finding's question was sent to the lookups. A finding
	// the diff settles is not, and neither is one this pull request has
	// already heard.
	Asked  bool            `json:"asked,omitempty"`
	Ruling findings.Ruling `json:"ruling,omitempty"`
	// EvidenceFrom says where the ruling's quote came from: "lookup" when it
	// is inside a range the scout filed, "elsewhere" when it is in the diff or
	// the findings the reviewer already had, empty when the ruling quoted
	// nothing. It is the closest thing to an answer to whether the lookups
	// found the thing that settled the finding, which is a different question
	// from whether they found anything.
	EvidenceFrom string `json:"evidenceFrom,omitempty"`
}

// Where a ruling's evidence came from.
const (
	FromLookup    = "lookup"
	FromElsewhere = "elsewhere"
)

// Lookup is the scout run that answered the review's questions.
type Lookup struct {
	// Ran says the scout was reached at all. False with an Error is a scout
	// that could not start, false without one is a review that never asked.
	Ran    bool   `json:"ran,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Error is why the lookups failed, when they did. The findings then went
	// out as the reviewer wrote them.
	Error string `json:"error,omitempty"`

	Turns     int     `json:"turns,omitempty"`
	CostUSD   float64 `json:"costUSD,omitempty"`
	CostKnown bool    `json:"costKnown,omitempty"`
	// CapHit says the search stopped at its own dollar cap rather than
	// because it was finished, which is the first thing to check when a
	// question came back unanswered.
	CapHit bool `json:"capHit,omitempty"`

	// Calls is every tool call in order: what it grepped for, what it read,
	// and every refusal it was corrected by.
	Calls []Call `json:"calls,omitempty"`
	// Filed is what it asked to send, and Resolved what the ruling was
	// actually shown. They differ when a file could not be read, when a
	// record duplicated another, or when the context budget dropped one.
	Filed    []Filed    `json:"filed,omitempty"`
	Resolved []Resolved `json:"resolved,omitempty"`
	Notes    []string   `json:"notes,omitempty"`
}

// Call is one tool call the scout made, and Filed one record it asked to
// send. Both mirror what the scout hands back rather than importing it: the
// scout is a context provider, Redline reads a provider's own types nowhere,
// and the command that runs it is the one place the two meet. See
// internal/boundary for why that rule is worth a copy of two structs.
type Call struct {
	// Turn is which model turn made the call, counting from one.
	Turn int    `json:"turn"`
	Tool string `json:"tool"`
	// Args is the call's input on one line, so a grep reads as its pattern.
	Args string `json:"args,omitempty"`
	// Result is the byte count on success and the error text on a refusal.
	// The refusal is the interesting half: it is a correction the scout was
	// given and either acted on or ran out of turns to act on.
	Result string `json:"result,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// Filed is one record the scout accepted, before anything read a line of it.
type Filed struct {
	Role      envelope.Role `json:"role"`
	File      string        `json:"file"`
	StartLine int           `json:"startLine,omitempty"`
	EndLine   int           `json:"endLine,omitempty"`
	Symbol    string        `json:"symbol,omitempty"`
	FoundVia  string        `json:"foundVia,omitempty"`
	// Answers is the finding id it was filed against. Empty on a run that was
	// given questions is a record the scout could not attach to any of them.
	Answers string `json:"answers,omitempty"`
}

// Resolved is one expansion that reached the ruling, read back from the
// envelope the scout produced.
type Resolved struct {
	Role      envelope.Role `json:"role"`
	File      string        `json:"file"`
	StartLine int           `json:"startLine,omitempty"`
	EndLine   int           `json:"endLine,omitempty"`
	Symbol    string        `json:"symbol,omitempty"`
	FoundVia  string        `json:"foundVia,omitempty"`
	// Answers is the finding id this range was fetched for. Empty is a range
	// the ruling saw and could not attach to any claim.
	Answers string `json:"answers,omitempty"`
	// Lines is how much of the file came with it.
	Lines int `json:"lines,omitempty"`
}

// Of assembles the trace from what a review returned. The caller fills in
// Target, Revision and Effort, which are the command's own knowledge rather
// than the producer's.
func Of(res *review.Result, look Lookup) *Trace {
	t := &Trace{
		SchemaVersion: SchemaVersion,
		Wrote:         time.Now().UTC(),
		API:           res.API,
		Model:         res.Model,
		Samples:       res.Samples,
		Verified:      res.Verified,
		VerifyFailed:  res.VerifyFailed,
		CostUSD:       res.CostUSD,
		CostKnown:     res.CostKnown,
		Lookup:        look,
	}
	// A review run with the checking pass off never numbered its findings, so
	// they are numbered here the same way the pass would have. The ids are
	// positional either way, and a postmortem that could not name a finding
	// could not say anything about it.
	cands := res.Candidates
	if len(cands) == 0 {
		cands = review.Candidates(res.Review)
	}
	asked := map[string]bool{}
	for _, q := range res.Questions {
		asked[q.ID] = true
	}
	for i, c := range cands {
		f := Finding{
			ID:         c.ID,
			File:       c.Comment.File,
			Line:       c.Comment.Line,
			Severity:   c.Comment.Severity,
			Confidence: c.Comment.Confidence,
			Body:       c.Comment.Body,
			Question:   c.Comment.Question,
			Asked:      asked[c.ID],
		}
		// The ruling is written onto the review in place, by the same index,
		// so it is read back from there rather than from the candidate, which
		// is the snapshot taken before any of it happened.
		if i < len(res.Review.Comments) {
			f.Ruling = res.Review.Comments[i].Ruling
		}
		f.EvidenceFrom = evidenceFrom(f.Ruling.Evidence, res.Answers)
		t.Findings = append(t.Findings, f)
	}
	t.Lookup.Resolved = resolvedOf(res.Answers)
	return t
}

// evidenceFrom says whether the line a ruling rested on was one the lookups
// fetched. The content of a range is in the envelope and nowhere else, so this
// is decided here, while the envelope is still in hand, and the answer is what
// the file carries.
//
// A finding kept on the diff alone is the common case and not a failure: the
// strongest question kind is the one the diff settles. What it is not is
// evidence that the scout did its job, and the two used to read the same.
func evidenceFrom(evidence string, env *envelope.Envelope) string {
	if strings.TrimSpace(evidence) == "" || env == nil {
		return ""
	}
	for _, x := range env.Expansions {
		if review.Quoted(evidence, x.Content) {
			return FromLookup
		}
	}
	return FromElsewhere
}

// resolvedOf reads back what the ruling was shown. The scout writes the role
// and the range on the expansion and the rest in details, which is where the
// envelope contract puts anything a provider wants to say about a range.
func resolvedOf(env *envelope.Envelope) []Resolved {
	if env == nil {
		return nil
	}
	out := make([]Resolved, 0, len(env.Expansions))
	for _, x := range env.Expansions {
		out = append(out, Resolved{
			Role: x.Role, File: x.File,
			StartLine: x.StartLine, EndLine: x.EndLine,
			Symbol:   x.Symbol,
			FoundVia: x.Details["foundVia"],
			Answers:  x.Details["answers"],
			Lines:    x.EndLine - x.StartLine + 1,
		})
	}
	return out
}

// Write saves the trace beside the rest of the evidence. A review that
// produced findings has done its job, so the caller treats a failure here as
// worth saying and not worth failing over.
func Write(dir string, t *Trace) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, File), append(buf, '\n'), 0o644)
}

// Load reads the trace of the last review in a directory.
func Load(dir string) (*Trace, error) {
	path := filepath.Join(dir, File)
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no review to look back on in %s (run `redline review` first)", dir)
		}
		return nil, err
	}
	var t Trace
	if err := json.Unmarshal(buf, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &t, nil
}
