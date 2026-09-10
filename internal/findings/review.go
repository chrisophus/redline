package findings

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Review is the agent's layer over a run, read from review.json. The agent reads
// findings.json, writes this file, and Redline merges it back on the next run.
// Redline never writes it, the way it never writes comments.json: it is the
// reviewer's own state, and a re-run must not clobber it.
//
// Overview and Files are prose about the change. Comments are line remarks that
// become findings marked source llm. Verdicts are judgments keyed by a finding's
// fingerprint.
type Review struct {
	// Revision is the change this review was written against, as
	// change.ReviewIdentity spells it. Stamped by `redline review`; empty in
	// a review written by hand, which is not an error but is not verifiable
	// either.
	//
	// It exists because review.json is merged onto whatever run comes next,
	// and nothing stopped that being a different change. A review of one
	// pull request rendered onto another, and was one command away from
	// being posted there.
	Revision string             `json:"revision,omitempty"`
	Overview string             `json:"overview,omitempty"`
	Files    map[string]string  `json:"files,omitempty"`
	Comments []ReviewComment    `json:"comments,omitempty"`
	Verdicts map[string]Verdict `json:"verdicts,omitempty"`
	// MutationVerdicts are judgments of surviving mutants, keyed by the
	// survivor's key (mutation.survived[].key in findings.json). Source llm.
	MutationVerdicts map[string]Verdict `json:"mutationVerdicts,omitempty"`
}

// ReviewComment is one remark the agent left on a line, shaped like a GitHub
// review comment. It becomes a finding so it renders on the diff line in the
// drawer beside Redline's own findings.
type ReviewComment struct {
	File      string   `json:"file"`
	Line      int      `json:"line,omitempty"`
	StartLine int      `json:"startLine,omitempty"`
	Side      string   `json:"side,omitempty"`
	Severity  Severity `json:"severity,omitempty"`
	Body      string   `json:"body"`
	// RelatedFindings are fingerprints from findings.json this comment
	// builds on. A comment that carries them is a correlation: it connects
	// facts the reviewer was given rather than restating one of them.
	RelatedFindings []string `json:"relatedFindings,omitempty"`
	// Confidence folds a weak remark away on the report without asking the
	// reviewer to withhold it.
	Confidence Confidence `json:"confidence,omitempty"`
	// Category overrides the default. Only "correlation" is accepted, in
	// whatever case it was written; every other value falls back to the
	// review default, because a reviewer labelling its own remark as a
	// schema measurement would put an opinion in the place the report
	// reserves for facts.
	Category Category `json:"category,omitempty"`
	// Question is what would confirm or refute this comment. See question.go
	// for why a finding has to carry one.
	Question Question `json:"question,omitempty"`
	// Ruling is what the verifying pass decided, when one ran. Zero means it
	// did not, and the comment is treated exactly as it was before the pass
	// existed.
	Ruling Ruling `json:"ruling,omitempty"`
}

// reviewWire is the on-disk shape before aliases and flexible fields normalize.
type reviewWire struct {
	Overview         string             `json:"overview"`
	WhatItDoes       string             `json:"what_it_does"`
	Files            json.RawMessage    `json:"files"`
	Comments         json.RawMessage    `json:"comments"`
	Findings         json.RawMessage    `json:"findings"`
	Verdicts         json.RawMessage    `json:"verdicts"`
	Revision         string             `json:"revision"`
	MutationVerdicts map[string]Verdict `json:"mutationVerdicts"`
}

type reviewCommentWire struct {
	File            string   `json:"file"`
	Path            string   `json:"path"`
	Line            int      `json:"line"`
	StartLine       int      `json:"startLine"`
	Side            string   `json:"side"`
	Severity        string   `json:"severity"`
	Body            string   `json:"body"`
	RelatedFindings []string `json:"relatedFindings"`
	Related         []string `json:"related"`
	Confidence      string   `json:"confidence"`
	Category        string   `json:"category"`
	Question        Question `json:"question"`
	Ruling          Ruling   `json:"ruling"`
}

type reviewFileEntry struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// StampReview records the change a review was written against, in place.
//
// The staleness guard can only refuse a review that says which change it is
// about, and the documented way for an agent or a person to write one is to
// put a file in `.redline/` by hand, which says nothing. That left the guard
// covering only the reviews `redline review` wrote itself: a hand-written
// review of one change was merged into the report of every later change,
// silently, which is the failure the guard exists to prevent and the one it
// could not see. Stamping the file the first time it is merged is what makes
// the second run able to refuse.
//
// The file is rewritten through a raw map so a field this version of Redline
// does not know is preserved: the file belongs to whoever wrote it, and this
// is adding one key to it, not taking it over.
func StampReview(path, revision string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	stamp, err := json.Marshal(revision)
	if err != nil {
		return err
	}
	raw["revision"] = stamp
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// LoadReview reads a review file. A missing file is not an error: most runs have
// no review yet. Every verdict and comment reads as source "llm" regardless of
// what the file claims: Redline attributes the reading, the file does not.
//
// Aliases accepted for cross-tool compatibility: what_it_does for overview;
// findings for comments; path for file; high/medium/low severities; related
// for relatedFindings; and any casing of the category.
func LoadReview(path string) (*Review, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var wire reviewWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	verdicts, err := parseReviewVerdicts(wire.Verdicts)
	if err != nil {
		return nil, fmt.Errorf("review verdicts: %w", err)
	}
	r := &Review{Verdicts: verdicts}
	r.Revision = strings.TrimSpace(wire.Revision)
	r.MutationVerdicts = wire.MutationVerdicts
	r.Overview = strings.TrimSpace(wire.Overview)
	if r.Overview == "" {
		r.Overview = strings.TrimSpace(wire.WhatItDoes)
	}
	files, err := parseReviewFiles(wire.Files)
	if err != nil {
		return nil, fmt.Errorf("review files: %w", err)
	}
	r.Files = files
	comments, err := parseReviewComments(wire.Comments, wire.Findings)
	if err != nil {
		return nil, fmt.Errorf("review comments: %w", err)
	}
	r.Comments = comments
	for k, v := range r.Verdicts {
		v.Source = SourceLLM
		r.Verdicts[k] = v
	}
	for k, v := range r.MutationVerdicts {
		v.Source = SourceLLM
		r.MutationVerdicts[k] = v
	}
	return r, nil
}

// parseReviewVerdicts accepts either shape, the same allowance files and
// comments already get. An agent writing review.json by hand keys them by
// fingerprint, which is what MergeVerdicts joins on. `redline review` emits an
// array instead, because a strict output schema cannot describe an object
// whose keys are fingerprints the model has never seen.
//
// A verdict missing its finding or its ruling is dropped rather than failing
// the load: the comments and the overview are the review, and one unattached
// ruling is worth losing on its own.
func parseReviewVerdicts(raw json.RawMessage) (map[string]Verdict, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asMap map[string]Verdict
	if err := json.Unmarshal(raw, &asMap); err == nil {
		return asMap, nil
	}
	var asList []reviewVerdictEntry
	if err := json.Unmarshal(raw, &asList); err != nil {
		return nil, fmt.Errorf("want object or array of {finding, ruling, rationale, fix}")
	}
	out := make(map[string]Verdict, len(asList))
	for _, e := range asList {
		id := strings.TrimSpace(e.Finding)
		ruling := strings.TrimSpace(e.Ruling)
		if id == "" || ruling == "" {
			continue
		}
		out[id] = Verdict{
			Ruling:    ruling,
			Rationale: strings.TrimSpace(e.Rationale),
			Fix:       strings.TrimSpace(e.Fix),
		}
	}
	return out, nil
}

// reviewVerdictEntry is the array form: the fingerprint moves from the key
// into the object.
type reviewVerdictEntry struct {
	Finding   string `json:"finding"`
	Ruling    string `json:"ruling"`
	Rationale string `json:"rationale"`
	Fix       string `json:"fix"`
}

func parseReviewFiles(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var asMap map[string]string
	if err := json.Unmarshal(raw, &asMap); err == nil {
		return asMap, nil
	}
	var asList []reviewFileEntry
	if err := json.Unmarshal(raw, &asList); err != nil {
		return nil, fmt.Errorf("want object or array of {path, summary}")
	}
	out := make(map[string]string, len(asList))
	for _, e := range asList {
		if e.Path == "" {
			continue
		}
		out[e.Path] = e.Summary
	}
	return out, nil
}

func parseReviewComments(commentsRaw, findingsRaw json.RawMessage) ([]ReviewComment, error) {
	raw := commentsRaw
	if len(raw) == 0 || string(raw) == "null" {
		raw = findingsRaw
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wires []reviewCommentWire
	if err := json.Unmarshal(raw, &wires); err != nil {
		return nil, err
	}
	out := make([]ReviewComment, 0, len(wires))
	for _, w := range wires {
		file := strings.TrimSpace(w.File)
		if file == "" {
			file = strings.TrimSpace(w.Path)
		}
		related := w.RelatedFindings
		if len(related) == 0 {
			related = w.Related
		}
		q := w.Question
		q.Kind = NormalizeQuestionKind(q.Kind)
		q.Ask, q.Subject = strings.TrimSpace(q.Ask), strings.TrimSpace(q.Subject)
		ruling := w.Ruling
		ruling.Verdict = NormalizeVerdict(ruling.Verdict)
		c := ReviewComment{
			File:            file,
			Line:            w.Line,
			StartLine:       w.StartLine,
			Side:            w.Side,
			Severity:        Severity(strings.TrimSpace(w.Severity)),
			Body:            w.Body,
			RelatedFindings: related,
			Confidence:      Confidence(strings.TrimSpace(w.Confidence)),
			Category:        normalizeCategory(Category(w.Category)),
			Question:        q,
			Ruling:          ruling,
		}
		// Written down as it will be read, through the same rule
		// CommentFindings applies. review.json is a file a person opens, and
		// one that recorded a finding as certain while every consumer treated
		// it as unsure would be lying to the only reader who cannot see the
		// consumers.
		c.Confidence = effectiveConfidence(c)
		out = append(out, c)
	}
	return out, nil
}

// CommentFindings turns the agent's line comments into findings so they carry a
// fingerprint, sit on their line in the drawer, and render beside the observed
// findings. Call before Finalize, which stamps the fingerprints. A comment with
// no body is dropped: it has nothing to say and nothing to key on.
func (r *Review) CommentFindings() []Finding {
	if r == nil {
		return nil
	}
	out := make([]Finding, 0, len(r.Comments))
	for _, c := range r.Comments {
		if c.Body == "" {
			continue
		}
		cat, rule := CategoryReview, "agent-comment"
		if c.Category == CategoryCorrelation {
			cat, rule = CategoryCorrelation, "correlation"
		}
		// The ruling reaches the page through Context, which the report and
		// the markdown already render as a quote under the finding. A reader
		// looking at a folded finding needs to know it was checked and what
		// came back, or the fold reads as the reviewer merely hedging.
		ctx := rulingContext(c.Ruling)
		sev := c.Severity
		if strings.TrimSpace(string(sev)) == "" {
			sev = cat.DefaultSeverity()
		}
		conf := effectiveConfidence(c)
		out = append(out, Finding{
			File:            c.File,
			Line:            c.Line,
			StartLine:       c.StartLine,
			Side:            normalizeSide(c.Side),
			Rule:            rule,
			Substrate:       "redline/review",
			Category:        cat,
			Severity:        normalizeSeverityFor(sev, cat),
			Message:         c.Body,
			Context:         ctx,
			Question:        c.Question,
			Source:          SourceLLM,
			RelatedFindings: c.RelatedFindings,
			Confidence:      conf,
		})
	}
	return out
}

// normalizeSeverity maps whatever the review file wrote onto the three
// severities the rest of the pipeline understands. Case and whitespace are
// forgiven; anything else falls back to the review default rather than
// flowing through verbatim, where an unknown value would outrank real errors
// in Sort, appear in no severity tile, and never match the post gate's
// blocking list.
// normalizeSeverityFor maps whatever the review file wrote onto the three
// severities, falling back to the category's own default rather than letting
// an unknown value through, where it would outrank real errors in Sort,
// appear in no severity tile, and never match the post gate's blocking list.
func normalizeSeverityFor(s Severity, cat Category) Severity {
	switch Severity(strings.ToLower(strings.TrimSpace(string(s)))) {
	case SeverityError, "high", "critical":
		return SeverityError
	case SeverityWarning, "medium", "med":
		return SeverityWarning
	case SeverityInfo, "low", "hint":
		return SeverityInfo
	}
	return cat.DefaultSeverity()
}

// normalizeCategory maps whatever the review file wrote onto the one category
// a reviewer may claim, forgiving case and whitespace the way severity and
// confidence are forgiven. The un-schema'd path an agent hand-writes spells
// the label however prose spells it, and "Correlation" taken verbatim matched
// nothing: the one finding a second wave exists to produce arrived as an
// ordinary remark, with the agent-comment rule, the review category and the
// severity that goes with it. Anything else reads as the review category, for
// the reason ReviewComment's Category field gives.
func normalizeCategory(c Category) Category {
	if Category(strings.ToLower(strings.TrimSpace(string(c)))) == CategoryCorrelation {
		return CategoryCorrelation
	}
	return CategoryReview
}

// normalizeSide maps a comment's diff side onto the two values GitHub accepts,
// forgiving case and whitespace. "LEFT" is a removed line on the old file;
// "RIGHT" is the new file. Anything else, including empty, reads as "" and the
// post layer defaults it to RIGHT, so a comment that named no side lands on
// the new file the way it always did.
func normalizeSide(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "LEFT":
		return "LEFT"
	case "RIGHT":
		return "RIGHT"
	default:
		return ""
	}
}

// MergeVerdicts attaches verdicts to findings by fingerprint. Call after
// Finalize, which stamps the fingerprints the verdicts key on. A verdict whose
// fingerprint matches no finding is dropped: it named a finding this run does
// not have, which the agent can see for itself in the report.
func (r *Report) MergeVerdicts(verdicts map[string]Verdict) {
	if len(verdicts) == 0 {
		return
	}
	for i := range r.Findings {
		if v, ok := verdicts[r.Findings[i].Fingerprint]; ok {
			vv := v
			r.Findings[i].Verdict = &vv
		}
	}
}

// rulingContext renders a ruling as the sentence a reader needs under the
// finding. Empty when no verifying pass ran, so a review from before this
// existed renders exactly as it used to.
func rulingContext(r Ruling) string {
	if r.Verdict == "" {
		return ""
	}
	var b strings.Builder
	switch r.Verdict {
	case VerifiedKept:
		b.WriteString("Checked and kept")
	case VerifiedWithdrawn:
		b.WriteString("Withdrawn: the evidence says otherwise")
	case VerifiedJustified:
		b.WriteString("True, and this repository does it on purpose")
	case VerifiedAlreadyRaised:
		b.WriteString("Already raised on this pull request")
	default:
		b.WriteString("Nothing available could settle this either way")
	}
	if r.Why != "" {
		b.WriteString(". " + r.Why)
	}
	if r.Evidence != "" {
		b.WriteString(" (" + r.Evidence + ")")
	}
	return b.String()
}

// effectiveConfidence is how sure a comment is once its own account of itself
// is taken into account.
//
// Two things override what the reviewer typed in the confidence field. A
// ruling that did not keep the finding, and a question of "none", which is the
// reviewer saying nothing available would settle its own claim. Both mean the
// finding must not reach an author, and low confidence is how the report and
// post already spell that.
//
// It lives here, at the conversion every consumer goes through, rather than in
// the deserializer where it started. The measurement is what moved it: a
// Review built in memory rather than read from disk skipped both rules, so a
// scoring harness could have reported a suppression that the product performs
// and the harness does not, or the reverse. A rule about what reaches an
// author has to hold however the review was built.
func effectiveConfidence(c ReviewComment) Confidence {
	if c.Ruling.Verdict != "" && !c.Ruling.Posts() {
		return ConfidenceLow
	}
	if c.Question.Kind == QuestionNone {
		return ConfidenceLow
	}
	return NormalizeConfidence(c.Confidence)
}
