package dispositions

import "time"

// Input types: what the sources (GitHub, git, Redline artifacts) hand to Derive. They carry no author or
// login, only whether the author is a bot, so nothing downstream can build a per-person metric.

// PR is one pull request as GitHub reports it.
type PR struct {
	Number      int
	State       string // OPEN, MERGED or CLOSED
	CreatedAt   time.Time
	ClosedAt    time.Time // zero while open
	AuthorIsBot bool
	// Commits are the PR's commits oldest first. The recorder needs them to tell a fix from a decline.
	Commits []Commit
	// Threads are the review threads, Redline's and everyone else's.
	Threads []Thread
	// ReviewBodies are the bodies of submitted reviews. Findings Redline could not anchor to a line ride here.
	ReviewBodies []string
	// IssueComments are the PR conversation comments, where the evidence comment lives.
	IssueComments []string
}

// Commit is one commit of a PR.
type Commit struct {
	SHA     string
	Time    time.Time
	IsMerge bool
}

// Thread is a review thread.
type Thread struct {
	Path       string
	Line       int // current line, 0 when the thread no longer maps onto the head
	OrigLine   int // line at the commit the first comment was made on
	IsResolved bool
	IsOutdated bool
	Comments   []ThreadComment
}

// Sources for findings that did not come from Redline: review threads without Redline markers, opened by
// another review bot (Copilot, the earlier agent reviewer) or by a person.
const (
	SourceBotReviewer   = "bot-reviewer"
	SourceHumanReviewer = "human-reviewer"
)

// ThreadComment is one comment in a thread.
type ThreadComment struct {
	Body      string
	CreatedAt time.Time
	// AuthorIsBot is whether the comment came from an app account (Redline, Copilot, other review bots). Only
	// the account type is read; no login is stored.
	AuthorIsBot bool
	// CommitSHA is the commit the comment was made on (GitHub's originalCommit), empty when GitHub has none.
	CommitSHA string
	// ByPRAuthor is whether the PR's author wrote the comment. Only their reply says what became of a finding.
	// The logins are compared when the PR is read and not kept.
	ByPRAuthor bool
}

// Run is one Redline review run, read from its CI artifact.
type Run struct {
	PRNumber   int
	RunID      int64
	ReviewedAt time.Time
	// ModelRan is whether a model review ran, which the run's reviews.jsonl records. Only such a run could have
	// asked a finding's question again, so only it can verify that a fix answered the question.
	ModelRan bool
	// Scope is the files the run examined. An incremental run (--since) lists only what changed since the
	// last review, which is why absence from a run proves nothing outside its scope.
	Scope    []string
	Findings []ArtifactFinding
}

// ArtifactFinding is one finding in a run's findings.json.
type ArtifactFinding struct {
	ID          string
	Fingerprint string
	File        string
	Line        int
	StartLine   int
	Rule        string
	Substrate   string
	Category    string
	Severity    string
	Source      string // llm or deterministic
	Confidence  string
	Verdict     string // verifying-pass verdict, empty when not recorded
	QuestionKey string // Redline's Question.Key(), empty when the question did not distinguish anything
}

// Differ answers git questions. The recorder implements it with `git diff`; tests use a fake.
type Differ interface {
	// Change returns what commit did to path against its first parent.
	Change(commit, path string) (FileChange, error)
}

// FileChange is what one commit did to one file.
type FileChange struct {
	// Hunks are the -U0 hunks, from path to RenamedTo when the file was renamed.
	Hunks   []Hunk
	Deleted bool
	// RenamedTo is the file's new path when the commit renamed it, else empty.
	RenamedTo string
}

// Disposition is what happened to a finding, evaluated in this order.
type Disposition string

const (
	DispNotPosted Disposition = "not-posted" // never became a review thread
	DispStale     Disposition = "stale"      // PR ended with the thread unresolved and no reply, or the file was deleted
	DispOpen      Disposition = "open"       // unresolved on a PR still open
	DispFixed     Disposition = "fixed"      // resolved with an Addressed reply, or a later commit changed the anchor
	DispDeclined  Disposition = "declined"   // resolved with a Not fixing reply, or no such change
	// DispUnknown is resolved, but the finding's commit is not in the PR's commit list (force-push or rebase),
	// so a fix cannot be told from a decline. It is kept apart so it does not pollute either rate.
	DispUnknown Disposition = "unknown"
)

// Anchor kinds and channels.
const (
	AnchorLine = "line"
	AnchorFile = "file"

	BasisReply    = "reply"
	BasisGeometry = "geometry"

	ReplyAddressed = "addressed"
	ReplyNotFixing = "not-fixing"

	ChannelThread     = "thread"
	ChannelReviewBody = "review-body"
	ChannelArtifact   = "artifact"
)

// FindingRow is one finding as said for one head commit (findings.jsonl).
type FindingRow struct {
	PrNumber      int       `json:"PrNumber"`
	HeadSha       string    `json:"HeadSha"`
	FindingKey    string    `json:"FindingKey"`
	FindingID     string    `json:"FindingId"`
	QuestionID    string    `json:"QuestionId"`
	Rule          string    `json:"Rule"`
	Substrate     string    `json:"Substrate"`
	Category      string    `json:"Category"`
	Severity      string    `json:"Severity"`
	Source        string    `json:"Source"`
	Confidence    string    `json:"Confidence"`
	VerifyVerdict string    `json:"VerifyVerdict"`
	File          string    `json:"File"`
	Line          int       `json:"Line"`
	Channel       string    `json:"Channel"`
	RunID         int64     `json:"RunId"`
	AuthorKind    string    `json:"AuthorKind"`
	RecordedAt    Timestamp `json:"RecordedAt"`
}

// DispositionRow is the outcome of one finding on one PR (dispositions.jsonl).
type DispositionRow struct {
	PrNumber    int    `json:"PrNumber"`
	FindingKey  string `json:"FindingKey"`
	File        string `json:"File"`
	QuestionID  string `json:"QuestionId"`
	FindingID   string `json:"FindingId"`
	Disposition string `json:"Disposition"`
	// Basis says what decided Disposition for a resolved thread: "reply" (the author's Addressed in <sha> or
	// Not fixing reply) or "geometry" (a later commit changed the anchor). Empty for other dispositions.
	Basis string `json:"Basis"`
	// ReplyKind is what the author's reply said (addressed, not-fixing) or empty. GeometryDisposition is what
	// the anchor check alone concluded. Keeping both lets the dashboard show how often they disagree.
	ReplyKind           string    `json:"ReplyKind"`
	GeometryDisposition string    `json:"GeometryDisposition"`
	VerifiedAddressed   uint8     `json:"VerifiedAddressed"`
	AnchorKind          string    `json:"AnchorKind"`
	Channel             string    `json:"Channel"`
	Severity            string    `json:"Severity"`
	Source              string    `json:"Source"`
	FirstHeadSha        string    `json:"FirstHeadSha"`
	FixCommitSha        string    `json:"FixCommitSha"`
	LastActivityAt      Timestamp `json:"LastActivityAt"`
	PrState             string    `json:"PrState"`
	AuthorKind          string    `json:"AuthorKind"`
	DerivedAt           Timestamp `json:"DerivedAt"`
}

// PRRow is one PR (prs.jsonl). HasEvidence is whether the PR carried an evidence comment (see RecordEvidence).
type PRRow struct {
	PrNumber            int        `json:"PrNumber"`
	AuthorKind          string     `json:"AuthorKind"`
	State               string     `json:"State"`
	CreatedAt           Timestamp  `json:"CreatedAt"`
	ClosedAt            *Timestamp `json:"ClosedAt"`
	ReviewRuns          int        `json:"ReviewRuns"`
	FindingsInThreads   int        `json:"FindingsInThreads"`
	HasEvidence         uint8      `json:"HasEvidence"`
	EvidenceVerified    int        `json:"EvidenceVerified"`
	EvidenceRefuted     int        `json:"EvidenceRefuted"`
	EvidenceNotVerified int        `json:"EvidenceNotVerified"`
	DerivedAt           Timestamp  `json:"DerivedAt"`
}

// Result is everything Derive produces for a PR.
type Result struct {
	PR           PRRow
	Findings     []FindingRow
	Dispositions []DispositionRow
}
