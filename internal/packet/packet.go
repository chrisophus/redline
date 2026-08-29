// Package packet is the contract between Redline and the agent reviewing a
// change. Redline emits a packet of facts; the agent returns judgments.
//
// The boundary is deliberate and narrow. Redline never calls a model — the
// prose and the judgment come from the session driving it, which is why this
// works identically in Claude Code and in Cursor. Redline's half is
// reproducible; the agent's half is labelled `source: "llm"` at every point of
// use so a reviewer always knows which is which.
package packet

import (
	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/instructions"
	"github.com/ccason/redline/internal/target"
)

// Version guards the packet encoding.
const Version = 1

// Packet is everything the agent needs to review the change, and nothing it
// needs to go looking for.
type Packet struct {
	Version int            `json:"version"`
	Target  *target.Target `json:"target"`
	BaseSHA string         `json:"baseSHA"`

	Commits []gitx.Commit   `json:"commits,omitempty"`
	Stats   []gitx.DiffStat `json:"stats,omitempty"`
	Files   []FileChange    `json:"files"`

	// Instructions are the repository's own review rules. The agent must
	// follow them; a review that contradicts the house style is noise.
	Instructions []instructions.File `json:"instructions,omitempty"`

	// Deterministic is what Redline already established by observation. The
	// agent must not re-derive or contradict these — they are evidence, not
	// opinion — and should not spend a finding on something already found.
	Deterministic []findings.Finding `json:"deterministic"`

	// Threads are narrative paths through the code graph touched by this
	// change, from graphify. They tell the agent what the changed code
	// connects to without it having to search.
	Threads []Thread `json:"threads,omitempty"`

	// Guidance is the review brief: what to look for and what to leave alone.
	Guidance Guidance `json:"guidance"`
}

// FileChange is one changed file with its diff.
type FileChange struct {
	Path     string `json:"path"`
	Status   string `json:"status"` // added | modified | deleted
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Diff     string `json:"diff,omitempty"`
	Language string `json:"language,omitempty"`
	// Areas classifies the file for the report's drill-in sections.
	Areas []string `json:"areas,omitempty"`
}

// Thread is one graph-derived path through the change.
type Thread struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	Nodes       []string `json:"nodes"`
	Explanation string   `json:"explanation,omitempty"`
}

// Guidance is the standing review brief. It ships in the binary rather than in
// a prompt so that what Redline asks for is versioned with Redline.
type Guidance struct {
	Focus   []string `json:"focus"`
	Avoid   []string `json:"avoid"`
	Output  string   `json:"output"`
	Sources string   `json:"sources"`
}

// DefaultGuidance is the review brief. It is tuned against the failure mode
// that makes AI review unusable: volume. A reviewer who must triage forty
// stylistic remarks to find the one real defect is worse off than with no
// review at all.
func DefaultGuidance() Guidance {
	return Guidance{
		Focus: []string{
			"Correctness defects: logic that does not do what the surrounding code and the PR description say it should.",
			"Contract changes not reflected everywhere they must be — a changed API shape, an added enum value with no handling, a renamed field still read by its old name.",
			"Error paths: newly added code that swallows, ignores, or misreports failure.",
			"Concurrency: shared state reached from more than one goroutine or request, and context that is not propagated.",
			"Security-relevant handling of untrusted input, credentials, and authorization checks on new endpoints.",
			"Violations of the repository's own instruction files, cited by file.",
			"Tests: behaviour the change introduces that no test exercises. Name the behaviour, not the coverage number.",
		},
		Avoid: []string{
			"Style, formatting, and naming, unless an instruction file demands it — the repository's linters own these.",
			"Restating what the diff plainly shows.",
			"Anything already reported in `deterministic` — those are observed facts and are shown to the reviewer separately.",
			"Speculative findings you cannot point at a line for. If you are unsure, say so in `confidence`, or leave it out.",
			"Praise, summaries of good practice, and encouragement. The reviewer's attention is the scarce resource.",
		},
		Output:  "Emit findings JSON on stdout and pipe it to `redline ingest`. Each finding needs file, line, rule, category, severity, message, and a concrete failure scenario in `context`.",
		Sources: "Everything you emit is recorded with source \"llm\" and rendered separately from Redline's observed findings. Do not claim to have executed anything.",
	}
}
