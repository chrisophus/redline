// Package history reads every review saved under a .redline directory and
// lays them side by side.
//
// A review makes three choices by fixed rule or by a flag somebody typed:
// whether the change goes to the model in one call, how it is split when it
// does not, and how long a pass keeps looking things up. Whether a smarter
// choice would have changed anything is a question about past reviews, and
// the answer is already on disk: the saved session has the change, the
// postmortem trace has what the review did, and the cost ledger has what it
// was allowed and what it spent. This package joins the three per review, and
// reads nothing outside the directory it is given. Collect calls no model.
// Report.Ask does: it puts the same choices to a model with only what was
// known at the time, so its answers can be set against what happened.
package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
)

// sessionsDir is where a .redline directory keeps one directory per target.
const sessionsDir = "sessions"

// headDirRE matches the directory a reviewed head's session is kept in, which
// is named by the full commit SHA.
var headDirRE = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

// ledgerWindow is how far apart a ledger row and a trace can be written and
// still be the same review. The command writes one straight after the other,
// so this only has to cover a slow disk.
const ledgerWindow = 30 * time.Second

// Run is one saved review.
type Run struct {
	// Session is the directory name under sessions, such as pr-131, and Head
	// the reviewed commit when the session was kept per head.
	Session string    `json:"session"`
	Head    string    `json:"head,omitempty"`
	Dir     string    `json:"dir"`
	When    time.Time `json:"when"`
	Target  string    `json:"target,omitempty"`
	Model   string    `json:"model,omitempty"`
	Effort  string    `json:"effort,omitempty"`

	// SizeKnown is false when the directory's session.json belongs to a
	// different change than the trace, which happens in a session directory
	// that was observed again after its last review.
	SizeKnown bool `json:"sizeKnown"`
	Files     int  `json:"files,omitempty"`
	Added     int  `json:"added,omitempty"`
	Removed   int  `json:"removed,omitempty"`
	// Dirs is how many directories the changed files sit in. It is the one
	// measure of spread that needs no model.
	Dirs int `json:"dirs,omitempty"`

	// InLedger is false when no cost row could be matched to the trace. The
	// fields down to Seconds come from that row and are zero without it.
	InLedger      bool    `json:"inLedger"`
	InputEstimate int     `json:"inputEstimate,omitempty"`
	Ceiling       int     `json:"ceiling,omitempty"`
	Pipeline      string  `json:"pipeline,omitempty"`
	Cohorts       int     `json:"cohorts,omitempty"`
	Seconds       float64 `json:"seconds,omitempty"`

	CostUSD   float64 `json:"costUSD"`
	CostKnown bool    `json:"costKnown"`
	Turns     int     `json:"turns,omitempty"`
	Verified  bool    `json:"verified,omitempty"`

	// Limits says in words each limit the review ran into. Empty is a review
	// that finished the way it was set up to.
	Limits []string `json:"limits,omitempty"`

	// Proposed is what the reviewer wrote and Kept what was left after the
	// ruling. They are equal on a review that did not run the ruling.
	Proposed int `json:"proposed"`
	Kept     int `json:"kept"`
	// Misses is how many recorded misses name this head.
	Misses int `json:"misses,omitempty"`

	Lookups Lookups `json:"lookups,omitzero"`
	Scout   Scout   `json:"scout,omitzero"`

	// Each is every lookup in the order the review made it, and Asked what a
	// model said when the review's choices were put to it. Asked is nil until
	// Report.Ask has run.
	Each  []Looked `json:"each,omitempty"`
	Asked *Asked   `json:"asked,omitempty"`

	// set is the change, kept for the questions Ask builds from it.
	set *change.Set
}

// Looked is one lookup a review made and what is known about how it went.
type Looked struct {
	Tool      string `json:"tool"`
	Input     string `json:"input,omitempty"`
	Bytes     int    `json:"bytes"`
	Empty     bool   `json:"empty,omitempty"`
	Repeated  bool   `json:"repeated,omitempty"`
	OnFinding bool   `json:"onFinding,omitempty"`
	// Said is the model's answer for this lookup, look or stop, and empty
	// when it was not asked or did not answer.
	Said string `json:"said,omitempty"`
	// StopP is the probability the model gave to stop, nil from an endpoint
	// that gives none.
	StopP *float64 `json:"stopP,omitempty"`
}

// Lines is the size of the change: lines added plus lines removed.
func (r Run) Lines() int { return r.Added + r.Removed }

// Split reports whether the review judged the change in more than one call.
func (r Run) Split() bool { return r.Cohorts > 1 }

// Lookups counts what a review's own passes looked up in the tree.
//
// The three counts below Made overlap: a lookup can be both a repeat and
// empty. Each is a share of Made on its own.
type Lookups struct {
	Made int `json:"made,omitempty"`
	// Empty came back with nothing found.
	Empty int `json:"empty,omitempty"`
	// Repeated asked the same tool the same thing as an earlier lookup in the
	// same review.
	Repeated int `json:"repeated,omitempty"`
	// OnFinding named a file the review filed a finding on. The trace does
	// not record which came first, so this says the lookup was about code the
	// review had something to say about, and not that it caused the finding.
	OnFinding int `json:"onFinding,omitempty"`
}

// Scout is what the lookups behind --verify did.
type Scout struct {
	Ran       bool    `json:"ran,omitempty"`
	Turns     int     `json:"turns,omitempty"`
	CostUSD   float64 `json:"costUSD,omitempty"`
	CapHit    bool    `json:"capHit,omitempty"`
	Calls     int     `json:"calls,omitempty"`
	Failed    int     `json:"failed,omitempty"`
	Repeated  int     `json:"repeated,omitempty"`
	Asked     int     `json:"asked,omitempty"`
	FromFetch int     `json:"fromFetch,omitempty"`
}

// Report is every saved review and what could not be read.
type Report struct {
	Root string `json:"root"`
	Runs []Run  `json:"runs"`
	// NoReview counts saved sessions that hold a run and no review of it.
	NoReview int `json:"noReview,omitempty"`
	// Notes are problems reading the directory that did not stop the rest.
	Notes []string `json:"notes,omitempty"`
	// Replay is the model run over these reviews, nil until Ask has run.
	Replay *Replay `json:"replay,omitempty"`
}

// Collect reads every review saved under root. ledgerDir holds reviews.jsonl
// and misses.jsonl, which is root unless they were kept elsewhere. A
// directory with no sessions is an empty report and not an error.
func Collect(root, ledgerDir string) (*Report, error) {
	rep := &Report{Root: root}
	ledger, err := review.ReadLedger(ledgerDir)
	if err != nil {
		rep.Notes = append(rep.Notes, "cost ledger not read in full: "+err.Error())
	}
	misses, err := miss.Read(ledgerDir)
	if err != nil {
		rep.Notes = append(rep.Notes, "misses not read: "+err.Error())
	}
	used := make([]bool, len(ledger))
	seen := map[string]bool{}
	for _, c := range candidates(root) {
		t, err := postmortem.Load(c.dir)
		if err != nil {
			if _, serr := os.Stat(filepath.Join(c.dir, "session.json")); serr == nil && c.head == "" {
				rep.NoReview++
			}
			continue
		}
		// A reviewed head is kept as a copy of the session it was written in,
		// so the newest review of a target is on disk twice.
		key := t.Revision + "|" + t.Wrote.Format(time.RFC3339Nano)
		if seen[key] {
			continue
		}
		seen[key] = true
		r := fromTrace(t)
		r.Session, r.Head, r.Dir = c.session, c.head, c.dir
		if res, err := run.LoadSession(c.dir); err == nil && res.Change != nil {
			if t.Revision == "" || change.ReviewIdentity(res.Report.BaseSHA, res.Change) == t.Revision {
				sizeOf(&r, res.Change)
				if r.Head == "" && res.Change.Target != nil {
					r.Head = res.Change.Target.Head
				}
			}
		}
		if i := matchLedger(ledger, used, t); i >= 0 {
			used[i] = true
			fromLedger(&r, ledger[i])
		}
		for _, m := range misses {
			if len(m.Head) >= 7 && r.Head != "" && strings.HasPrefix(r.Head, m.Head) {
				r.Misses++
			}
		}
		rep.Runs = append(rep.Runs, r)
	}
	sort.SliceStable(rep.Runs, func(i, j int) bool { return rep.Runs[i].When.Before(rep.Runs[j].When) })
	return rep, nil
}

type candidate struct {
	dir, session, head string
}

// candidates lists every directory that can hold a review: each kept head of
// each session first, then the session itself, then root, which is where a
// session lived before there was one directory per target.
func candidates(root string) []candidate {
	var out []candidate
	sessions, _ := os.ReadDir(filepath.Join(root, sessionsDir))
	for _, s := range sessions {
		if !s.IsDir() {
			continue
		}
		dir := filepath.Join(root, sessionsDir, s.Name())
		heads, _ := os.ReadDir(dir)
		for _, h := range heads {
			if h.IsDir() && headDirRE.MatchString(h.Name()) {
				out = append(out, candidate{dir: filepath.Join(dir, h.Name()), session: s.Name(), head: h.Name()})
			}
		}
		out = append(out, candidate{dir: dir, session: s.Name()})
	}
	return append(out, candidate{dir: root, session: "."})
}

func fromTrace(t *postmortem.Trace) Run {
	r := Run{
		When: t.Wrote, Target: t.Target, Model: t.Model, Effort: t.Effort,
		CostUSD: t.CostUSD, CostKnown: t.CostKnown,
		Turns: t.CallTurns, Verified: t.Verified,
		Proposed: len(t.Findings),
	}
	files := map[string]bool{}
	for _, f := range t.Findings {
		if !t.Verified || f.Ruling.Posts() {
			r.Kept++
		}
		if f.File != "" {
			files[f.File] = true
		}
		if f.Asked {
			r.Scout.Asked++
		}
		if f.EvidenceFrom == postmortem.FromLookup {
			r.Scout.FromFetch++
		}
	}
	if len(t.Stopped) > 0 {
		r.Limits = append(r.Limits, "stopped before done: "+strings.Join(t.Stopped, ", "))
	}

	asked := map[string]bool{}
	for _, l := range t.Lookups {
		e := Looked{Tool: l.Tool, Input: l.Input, Bytes: l.Bytes, Empty: l.Empty}
		k := l.Tool + "\x00" + normalize(l.Input)
		e.Repeated = asked[k]
		asked[k] = true
		for f := range files {
			if namesFile(l.Input, f) {
				e.OnFinding = true
				break
			}
		}
		r.Lookups.Made++
		if e.Empty {
			r.Lookups.Empty++
		}
		if e.Repeated {
			r.Lookups.Repeated++
		}
		if e.OnFinding {
			r.Lookups.OnFinding++
		}
		r.Each = append(r.Each, e)
	}

	s := t.Lookup
	r.Scout.Ran, r.Scout.Turns, r.Scout.CapHit = s.Ran, s.Turns, s.CapHit
	if s.CostKnown {
		r.Scout.CostUSD = s.CostUSD
	}
	called := map[string]bool{}
	for _, c := range s.Calls {
		r.Scout.Calls++
		if c.Failed {
			r.Scout.Failed++
		}
		k := c.Tool + "\x00" + normalize(c.Args)
		if called[k] {
			r.Scout.Repeated++
		}
		called[k] = true
	}
	if s.CapHit {
		r.Limits = append(r.Limits, "the lookups behind --verify stopped at their cost cap")
	}
	return r
}

// namesFile reports whether a lookup's input names the file. The path has to
// stand as a whole path: a finding on a.go is not named by a lookup of
// data.go, and one on x/a.go is not named by a lookup of vendor/x/a.go.
func namesFile(input, file string) bool {
	if file == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(input[from:], file)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(file)
		if (start == 0 || !inPath(input[start-1])) && (end == len(input) || !inPath(input[end])) {
			return true
		}
		from = start + 1
	}
}

// inPath reports whether a byte can be part of a file path, so that a path
// with one on either side of it is a piece of a longer path.
func inPath(c byte) bool {
	return c == '/' || c == '.' || c == '_' || c == '-' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// normalize makes two spellings of one lookup compare equal. The input is the
// call's arguments as JSON, so it is decoded and written back, which drops
// differences in spacing and key order. Case is kept: a search for Reset and
// one for RESET are different questions.
func normalize(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		if buf, err := json.Marshal(v); err == nil {
			return string(buf)
		}
	}
	return strings.Join(strings.Fields(s), " ")
}

func sizeOf(r *Run, set *change.Set) {
	r.SizeKnown = true
	r.set = set
	dirs := map[string]bool{}
	for _, f := range set.Files {
		r.Files++
		r.Added += f.Added
		r.Removed += f.Removed
		dirs[filepath.Dir(f.Path)] = true
	}
	r.Dirs = len(dirs)
}

// matchLedger finds the cost row written by the same review as the trace: the
// nearest unused row for the same model inside the window. The ledger does
// not name the change a row is about, so the time is all there is to join on.
func matchLedger(ledger []review.Entry, used []bool, t *postmortem.Trace) int {
	best, bestGap := -1, ledgerWindow+1
	for i, e := range ledger {
		if used[i] || e.Model != t.Model {
			continue
		}
		gap := t.Wrote.Sub(e.At)
		if gap < 0 {
			gap = -gap
		}
		if gap < bestGap {
			best, bestGap = i, gap
		}
	}
	return best
}

func fromLedger(r *Run, e review.Entry) {
	r.InLedger = true
	r.InputEstimate, r.Ceiling = e.InputEstimate, e.Ceiling
	r.Pipeline, r.Cohorts, r.Seconds = e.Shape(), e.Cohorts, e.Seconds
	if e.Truncated {
		r.Limits = append(r.Limits, "output cut off at the token cap")
	}
	if e.OverCeiling {
		r.Limits = append(r.Limits, "prompt over the size ceiling, so part of it was cut")
	}
	if e.CohortsFailed > 0 {
		r.Limits = append(r.Limits, plural(e.CohortsFailed, "cohort call")+" failed")
	}
	if e.FellBack != "" {
		r.Limits = append(r.Limits, "split abandoned for one call: "+e.FellBack)
	}
}
