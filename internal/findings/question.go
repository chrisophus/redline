package findings

import "strings"

// A finding says what is wrong. The question says what would show whether it
// is, and it is the difference between a review that can be checked and one
// that can only be believed.
//
// It exists because of a measured failure. Field use produced reviews whose
// every finding the reader dismissed, most of them accurate observations about
// code the repository had deliberately written that way. The reviewer had no
// way to check: it is one call with no tools, so a claim about what the rest
// of the repository does was a guess with a confidence label on it. The reader
// checked in seconds, because the reader had the repository open.
//
// So the reviewer names the check rather than performing it. A lookup pass
// runs the checks afterwards, and a second pass rules on each finding with
// the answers in front of it. Naming the check is also a filter on its own: a
// model asked how its claim could be falsified writes fewer claims that cannot
// be.
//
// The kinds are a closed set on purpose, and the set is what the scout's tools
// can actually answer. An open field would collect "is this a good idea",
// which nothing can look up, and the pipeline behind it would have to guess
// what to do with that.

// QuestionKind is what sort of lookup would settle a finding.
type QuestionKind string

const (
	// QuestionDiff is a finding the material already in front of the reviewer
	// settles: a swallowed error visible in the hunk, two lines of the diff
	// that contradict each other. Nothing needs fetching, and this is the
	// strongest kind rather than the weakest.
	QuestionDiff QuestionKind = "diff"
	// QuestionPrecedent asks whether the repository already does this
	// elsewhere, unchanged by this change. The kind that would have caught
	// the dismissals: answered by a grep and a read.
	QuestionPrecedent QuestionKind = "precedent"
	// QuestionCaller asks what calls or reads the thing that changed.
	QuestionCaller QuestionKind = "caller"
	// QuestionRule asks whether the team wrote a rule about this.
	QuestionRule QuestionKind = "rule"
	// QuestionHistory asks why the removed code was there.
	QuestionHistory QuestionKind = "history"
	// QuestionType asks what a type can represent.
	QuestionType QuestionKind = "type"
	// QuestionNone is the honest answer when nothing available would settle
	// it. A finding carrying it is speculation by its author's own account,
	// so it is folded to low confidence and never posted. It is not dropped:
	// the report promises an uncertain finding costs the reader nothing and
	// a withheld one costs them the finding, and that promise holds here.
	QuestionNone QuestionKind = "none"
)

// Question is one check that would confirm or refute a finding.
type Question struct {
	Kind QuestionKind `json:"kind"`
	// Ask is the question in words, for a person reading the report and for
	// the model that has to answer it.
	Ask string `json:"ask,omitempty"`
	// Subject is what to look up: a symbol, a path, a pattern. It is what
	// makes two samples describing one defect in different words recognisable
	// as one finding, which no amount of comparing prose could do.
	Subject string `json:"subject,omitempty"`
}

// Answerable reports whether a lookup would settle this finding, which is
// what decides if it is worth a scout turn. A finding the diff already
// settles needs nothing, and one nothing can settle gets nothing.
func (q Question) Answerable() bool {
	switch q.Kind {
	case QuestionPrecedent, QuestionCaller, QuestionRule, QuestionHistory, QuestionType:
		return q.Subject != "" || q.Ask != ""
	}
	return false
}

// Key is the identity two samples of one defect share.
//
// Redline measured no overlap at all across forty samples and concluded that
// agreement could not be a confidence signal. That conclusion was about the
// identity in use, which was the finding's own wording: two samples describing
// one defect in different words counted as two findings, so of course they
// never agreed. A question is the same for both, and it makes the measurement
// worth taking again.
//
// Empty when the question does not distinguish anything, and the caller falls
// back to comparing prose.
func (q Question) Key() string {
	if q.Kind == "" || q.Kind == QuestionNone || q.Subject == "" {
		return ""
	}
	return string(q.Kind) + "\x00" + strings.ToLower(strings.TrimSpace(q.Subject))
}

// NormalizeQuestionKind maps whatever a review file wrote onto the closed set,
// forgiving case and whitespace the way severity and confidence are forgiven.
// An unknown kind reads as unstated rather than flowing through: a kind
// nothing can act on is worse than none, because the stages behind this would
// try to act on it.
func NormalizeQuestionKind(k QuestionKind) QuestionKind {
	switch QuestionKind(strings.ToLower(strings.TrimSpace(string(k)))) {
	case QuestionDiff:
		return QuestionDiff
	case QuestionPrecedent:
		return QuestionPrecedent
	case QuestionCaller:
		return QuestionCaller
	case QuestionRule:
		return QuestionRule
	case QuestionHistory:
		return QuestionHistory
	case QuestionType:
		return QuestionType
	case QuestionNone:
		return QuestionNone
	}
	return ""
}

// Verdict words the verifying pass uses. Distinct from the three the report
// already has for ruling on a pane's finding, because these answer a different
// question: not "is this check right about my code" but "should this claim
// reach the author at all".
const (
	// VerifiedKept is a finding the evidence supports. The only one that is
	// posted.
	VerifiedKept = "kept"
	// VerifiedWithdrawn is a finding the evidence refutes.
	VerifiedWithdrawn = "withdrawn"
	// VerifiedJustified is a finding that is true and that this repository
	// does on purpose, established by precedent or by the author saying so on
	// an earlier review.
	//
	// The word the field evidence asked for. Of four dismissed findings on one
	// staging change, three were accurate observations the team had already
	// accepted because the file next door made the same choice. Withdrawn
	// would have been a lie and posting would have been noise.
	VerifiedJustified = "justified"
	// VerifiedUnverifiable is a finding nothing could settle either way.
	VerifiedUnverifiable = "unverifiable"
	// VerifiedAlreadyRaised is a finding this pull request has already heard.
	VerifiedAlreadyRaised = "already-raised"
)

// Ruling is what the verifying pass decided about one comment.
type Ruling struct {
	Verdict string `json:"verdict"`
	// Evidence is the line the decision rests on, quoted from the material.
	// A ruling with no evidence is an opinion about an opinion, which is what
	// this stage exists to replace.
	Evidence string `json:"evidence,omitempty"`
	Why      string `json:"why,omitempty"`
	// Analysis is the reasoning the ruling wrote before it chose the verdict,
	// kept so the report can show the working rather than only the conclusion.
	Analysis string `json:"analysis,omitempty"`
	// Grounded says the evidence above was found in the material the ruling
	// was shown, rather than written from memory. The verifying pass sets it;
	// a ruling that arrived any other way leaves it false, and false is read
	// as unchecked rather than as refuted.
	Grounded bool `json:"grounded,omitempty"`
}

// Posts reports whether a comment carrying this ruling should reach the
// author. Only a kept finding does. An unruled comment posts too: a review
// that never went through the verifying pass is the old behaviour, and it must
// not be silently emptied by a stage that did not run.
func (r Ruling) Posts() bool {
	return r.Verdict == "" || r.Verdict == VerifiedKept
}

// NormalizeVerdict maps whatever was written onto the closed set, forgiving
// case and whitespace. An unknown verdict reads as unverifiable rather than as
// kept: a ruling nobody can read must not be the one that lets a finding
// through.
func NormalizeVerdict(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return ""
	case VerifiedKept:
		return VerifiedKept
	case VerifiedWithdrawn:
		return VerifiedWithdrawn
	case VerifiedJustified:
		return VerifiedJustified
	case VerifiedAlreadyRaised, "already raised", "duplicate":
		return VerifiedAlreadyRaised
	}
	return VerifiedUnverifiable
}
