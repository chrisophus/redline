// Package packet is the contract between Redline and the agent reviewing a
// change. Redline emits a packet of facts; the agent returns judgments.
//
// The boundary is deliberate and narrow. Redline does not call a model unless
// asked to (`--with` for a reviewer, `--brief` for a context pass). The prose
// and the judgment come from the session driving it, which is why this works
// identically in Claude Code and in Cursor. Redline's half is reproducible;
// the agent's half is labelled `source: "llm"` at every point of use so a
// reviewer always knows which is which.
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

	// Brief is the context-gathering pass, present when `--brief` was asked for
	// and the pass wrote something. It is what lets the reviewing agent see
	// callers, docs, and tests outside the diff without rediscovering them.
	Brief *Brief `json:"brief,omitempty"`

	// UITouched is true when at least one changed file is classified as UI.
	// The skill uses this to decide whether to walk the interface; it is a
	// fact about the packet, not a finding.
	UITouched bool `json:"uiTouched"`
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
			"Orientation, as review `intent`: the ticket this change serves (`intent.ticket`), and `intent.fit` — whether this is the thing the ticket asked for (`thing`) and whether this is the way to build it (`way`). The reviewer reads that before anything else and the diff cannot supply it.",
			"What the change actually does, as review `actual`, compared to the pull request body already in `target.pr`. Every place they disagree goes in `discrepancies` and as a finding with `category: \"intent\"` and the same `rule`, so the posted overview can thread to the inline comment.",
			"One line per contract surface, as review `surfaces`: `interface`, `api`, `schema`, each `{line, moved}`. A surface that did not move still takes a line saying what you looked at — silence there reads as a pass and must not.",
			"Info-level findings worth addressing. Warning and error lint is gated in CI and already fixed before this review; what is useful here is the info-level remark a reviewer would want to know about.",
			"Correctness defects: logic that does not do what the surrounding code and the PR description say it should.",
			"Contract changes not reflected everywhere they must be — a changed API shape, an added enum value with no handling, a renamed field still read by its old name.",
			"Error paths: newly added code that swallows, ignores, or misreports failure.",
			"Concurrency: shared state reached from more than one goroutine or request, and context that is not propagated.",
			"Security-relevant handling of untrusted input, credentials, and authorization checks on new endpoints.",
			"Violations of the repository's own instruction files, cited by file.",
			"Tests: behaviour the change introduces that no test exercises. Name the behaviour, not the coverage number.",
			"When `uiTouched` is true: drive the changed journeys in a real browser with agent-browser, capture screenshots, and report defects you actually saw. Diff-scoped — not a whole-app crawl. Attach them as `screenshots`. Category `ui`.",
			"A one-sentence summary of every path in this packet's `files[]`, returned as review `files` (`path` + `summary`). This is the walkthrough a reviewer reads first — what each file does in this change, not a restatement of the hunk.",
			"When `brief` is present: act on it. Follow `brief.references` to the callers and callees outside the diff, read `brief.docsNaming` for contradictions between the docs and the code, and check `brief.testsCovering` for gaps. Findings that rest on the brief should cite the path they came from. Report in `unknowns` any brief item you could not check.",
		},
		Avoid: []string{
			"Style, formatting, and naming, unless an instruction file demands it — the repository's linters own these.",
			"Restating hunks inside findings. The file-by-file `files[]` walkthrough is required; findings are for defects.",
			"Anything already reported in `deterministic` — those are observed facts and are shown to the reviewer separately.",
			"Speculative findings you cannot point at a line for. If you are unsure, say so in `confidence`, or leave it out.",
			"Praise, summaries of good practice, and encouragement. The reviewer's attention is the scarce resource.",
			"Presenting an agent UI walk as Redline checks 15–16. Those are not built. Your walk is source llm.",
		},
		Output:  "Emit findings JSON on stdout and pipe it to `redline ingest`. Include `intent` (ticket, fit) and `surfaces` (interface, api, schema) — those are the top of the screen. Include `files`: one `{path, summary}` for every path in this packet. Each finding needs file, line, rule, category, severity, message, and a concrete failure scenario in `context`. Include `screenshots` when you walked the UI.",
		Sources: "Findings you emit are recorded with source \"llm\" and rendered apart from observed ones. Do not claim Redline executed or tested anything. A UI walk you drove is your work: say so in context and attach screenshots[].",
	}
}
