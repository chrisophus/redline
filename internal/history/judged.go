package history

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/jev"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/postmortem"
	"github.com/chrisophus/redline/internal/review"
	"github.com/chrisophus/redline/internal/run"
)

// The three replays in this file put a yes/no question to a decision model
// about things a saved review already holds: whether one of its findings
// described a defect that was later recorded as missed, whether the code a
// ruling quoted shows what the finding claims, and which context entries the
// review needed. None of them changes a review. Each exists to show, on
// reviews that already ran, whether the model's answer is worth acting on
// before anything is built that acts on it.

// MissBar and EvidenceBar are where a probability is read as yes. Neither has
// been set against labelled cases, so each report prints the scores beside
// the verdicts for a reader to check.
const (
	MissBar     = 0.5
	EvidenceBar = 0.5
)

// Caps on what one review sends, so a replay's cost is bounded by the number
// of reviews and not by the largest one.
const (
	maxMissFindings   = 40
	maxContextEntries = 40
	// weakestShown is how many kept findings the evidence section names.
	weakestShown = 5
)

// MissJudge says how likely each finding is to describe a defect.
type MissJudge interface {
	Describes(ctx context.Context, d jev.Defect, said []jev.Said) ([]float64, error)
}

// EvidenceJudge says how likely a claim's evidence is to show what its
// finding says.
type EvidenceJudge interface {
	Supports(ctx context.Context, c jev.Claim) (float64, error)
}

// ContextJudge says how likely a reviewer is to need each context entry.
type ContextJudge interface {
	Needed(ctx context.Context, files []jev.Changed, entries []jev.Entry) ([]float64, error)
}

// What became of a recorded miss in the review it was recorded against.
const (
	// MissFound is a finding that described the defect and that the ruling
	// did not withdraw. It can still have been held back when posting, for
	// low confidence or hedged wording, which the trace does not record.
	MissFound = "found"
	// MissRuledOut is a finding that described the defect and was withdrawn
	// by the ruling.
	MissRuledOut = "ruled out"
	// MissNotProposed is a review none of whose findings described it.
	MissNotProposed = "not proposed"
	// MissNoReview is a miss with no saved review of its head.
	MissNoReview = "no saved review"
)

// MissSeen is one recorded miss and the finding that came closest to it.
type MissSeen struct {
	PR      int    `json:"pr"`
	Head    string `json:"head,omitempty"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Summary string `json:"summary"`
	Verdict string `json:"verdict"`
	// Score is the highest probability any finding got, and Finding the id
	// and text of the finding that got it. Empty when the review had none.
	Score      float64 `json:"score,omitempty"`
	Finding    string  `json:"finding,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
	Text       string  `json:"text,omitempty"`
}

// MissTotals is the misses replay.
type MissTotals struct {
	Seen   []MissSeen `json:"seen,omitempty"`
	Failed string     `json:"failed,omitempty"`
}

// ReplayMisses asks, for each recorded miss, whether any finding of the
// review it was recorded against described the defect. Matching by file and
// line, which is what `redline miss` does, counts a finding on the right line
// about something else and misses a finding two lines away about the defect.
func (rep *Report) ReplayMisses(ctx context.Context, judge MissJudge, misses []miss.Entry, progress func(done, total int)) {
	tot := &MissTotals{}
	rep.Misses = tot
	for n, m := range misses {
		seen := MissSeen{PR: m.PR, Head: m.Head, File: m.File, Line: m.Line, Summary: m.Summary, Verdict: MissNoReview}
		if r := rep.runAt(m.Head); r != nil {
			seen.Verdict = MissNotProposed
			if err := missOne(ctx, judge, r, m, &seen); err != nil && tot.Failed == "" {
				tot.Failed = err.Error()
			}
		}
		tot.Seen = append(tot.Seen, seen)
		if progress != nil {
			progress(n+1, len(misses))
		}
	}
}

// runAt is the saved review of a head, which a miss may name by a prefix.
func (rep *Report) runAt(head string) *Run {
	if len(head) < 7 {
		return nil
	}
	for i := range rep.Runs {
		if strings.HasPrefix(rep.Runs[i].Head, head) {
			return &rep.Runs[i]
		}
	}
	return nil
}

func missOne(ctx context.Context, judge MissJudge, r *Run, m miss.Entry, seen *MissSeen) error {
	trace, err := postmortem.Load(r.Dir)
	if err != nil {
		return nil //nolint:nilerr // a review with no trace proposed nothing this can read
	}
	var fs []postmortem.Finding
	for _, f := range trace.Findings {
		if strings.TrimSpace(f.Body) != "" {
			fs = append(fs, f)
		}
	}
	// The defect's own file first, so the cap cuts the findings least likely
	// to be about it.
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].File == m.File && fs[j].File != m.File })
	if len(fs) > maxMissFindings {
		fs = fs[:maxMissFindings]
	}
	if len(fs) == 0 {
		return nil
	}
	said := make([]jev.Said, len(fs))
	for i, f := range fs {
		said[i] = jev.Said{File: f.File, Line: f.Line, Text: f.Body}
	}
	probs, err := judge.Describes(ctx, jev.Defect{File: m.File, Line: m.Line, Summary: m.Summary}, said)
	if err != nil {
		return err
	}
	best := -1
	for i, p := range probs {
		if i < len(fs) && (best < 0 || p > probs[best]) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	f := fs[best]
	seen.Score, seen.Finding, seen.Text, seen.Confidence = probs[best], f.ID, f.Body, string(f.Confidence)
	if probs[best] >= MissBar {
		seen.Verdict = MissRuledOut
		if f.Ruling.Posts() {
			seen.Verdict = MissFound
		}
	}
	return nil
}

// EvidenceSeen is one ruled finding and how well its quoted evidence
// supports it.
type EvidenceSeen struct {
	Session  string  `json:"session"`
	Head     string  `json:"head,omitempty"`
	Finding  string  `json:"finding"`
	Verdict  string  `json:"verdict"`
	Supports float64 `json:"supports"`
	Text     string  `json:"text,omitempty"`
	Evidence string  `json:"evidence,omitempty"`
}

// EvidenceTotals is the evidence replay.
type EvidenceTotals struct {
	Seen   []EvidenceSeen `json:"seen,omitempty"`
	Failed string         `json:"failed,omitempty"`
}

// ReplayEvidence asks, for every saved finding whose ruling quoted a line,
// whether that line shows what the finding claims. A ruling is marked
// grounded when its quote was in the material, and that is true of a real
// line that says something else.
func (rep *Report) ReplayEvidence(ctx context.Context, judge EvidenceJudge, progress func(done, total int)) {
	tot := &EvidenceTotals{}
	rep.Evidence = tot
	for n, r := range rep.Runs {
		trace, err := postmortem.Load(r.Dir)
		if err == nil {
			for _, f := range trace.Findings {
				if strings.TrimSpace(f.Ruling.Evidence) == "" || strings.TrimSpace(f.Body) == "" {
					continue
				}
				p, err := judge.Supports(ctx, jev.Claim{
					Finding: jev.Said{File: f.File, Line: f.Line, Text: f.Body}, Evidence: f.Ruling.Evidence})
				if err != nil {
					if tot.Failed == "" {
						tot.Failed = err.Error()
					}
					continue
				}
				tot.Seen = append(tot.Seen, EvidenceSeen{Session: r.Session, Head: r.Head, Finding: f.ID,
					Verdict: f.Ruling.Verdict, Supports: p, Text: f.Body, Evidence: f.Ruling.Evidence})
			}
		}
		if progress != nil {
			progress(n+1, len(rep.Runs))
		}
	}
}

// ContextTotals is the context replay.
//
// An entry counts as used when a ruling quoted a line from it or a finding
// named its symbol. That undercounts: a reviewer can read an entry, be
// reassured by it and write nothing. So a low score on a used entry is a
// real loss, and a high score on an unused one is not shown to be a mistake.
type ContextTotals struct {
	// Reviews is how many reviews had both a used entry and an unused one,
	// which is what it takes to compare the two, and Skipped how many saved
	// reviews with context did not.
	Reviews int `json:"reviews"`
	Skipped int `json:"skipped,omitempty"`
	Entries int `json:"entries"`
	Used    int `json:"used"`
	// MeanUsed and MeanOther are the average probability the model gave the
	// two kinds of entry.
	MeanUsed  float64 `json:"meanUsed"`
	MeanOther float64 `json:"meanOther"`
	// Pairs is every used entry paired with every unused entry of the same
	// review, and Right how many of those pairs scored the used one higher.
	// A tie counts as half.
	Pairs int     `json:"pairs"`
	Right float64 `json:"right"`
	// KeptInTopHalf is how many used entries were in the higher-scoring half
	// of their review's entries.
	KeptInTopHalf int    `json:"keptInTopHalf"`
	Failed        string `json:"failed,omitempty"`
}

// ReplayContext asks, for each saved review, which of the context entries
// its session resolved a reviewer would need, and checks the answers against
// the entries the review turned out to use.
func (rep *Report) ReplayContext(ctx context.Context, judge ContextJudge, progress func(done, total int)) {
	tot := &ContextTotals{}
	rep.Context = tot
	var sumUsed, sumOther float64
	for n, r := range rep.Runs {
		if r.SizeKnown {
			used, scores, err := contextOne(ctx, judge, r.Dir)
			switch {
			case err != nil:
				if tot.Failed == "" {
					tot.Failed = err.Error()
				}
			case scores == nil:
				if used != nil {
					tot.Skipped++
				}
			default:
				tot.tally(used, scores, &sumUsed, &sumOther)
			}
		}
		if progress != nil {
			progress(n+1, len(rep.Runs))
		}
	}
	if tot.Used > 0 {
		tot.MeanUsed = sumUsed / float64(tot.Used)
	}
	if other := tot.Entries - tot.Used; other > 0 {
		tot.MeanOther = sumOther / float64(other)
	}
}

// contextOne scores one review's entries. used is nil for a review with no
// context at all, and scores is nil when nothing was asked: a review whose
// entries were all used or all unused has nothing to compare.
func contextOne(ctx context.Context, judge ContextJudge, dir string) (used []bool, scores []float64, err error) {
	res, lerr := run.LoadSession(dir)
	trace, terr := postmortem.Load(dir)
	if lerr != nil || terr != nil || res.Change == nil {
		return nil, nil, nil
	}
	var entries []jev.Entry
	for _, env := range res.Envelopes {
		if env == nil {
			continue
		}
		for _, x := range env.Expansions {
			if strings.TrimSpace(x.Content) == "" || len(entries) == maxContextEntries {
				continue
			}
			entries = append(entries, jev.Entry{Role: string(x.Role), File: x.File, Symbol: x.Symbol, Content: x.Content})
		}
	}
	if len(entries) == 0 {
		return nil, nil, nil
	}
	used = make([]bool, len(entries))
	any, all := false, true
	for i, e := range entries {
		used[i] = usedBy(e, trace.Findings)
		any = any || used[i]
		all = all && used[i]
	}
	if !any || all {
		return used, nil, nil
	}
	files := make([]jev.Changed, len(res.Change.Files))
	for i, f := range res.Change.Files {
		files[i] = jev.Changed{Path: f.Path, Added: f.Added, Removed: f.Removed, Diff: f.Diff}
	}
	scores, err = judge.Needed(ctx, files, entries)
	if err == nil && len(scores) != len(entries) {
		err = fmt.Errorf("got %d scores for %d context entries", len(scores), len(entries))
	}
	return used, scores, err
}

// minSymbol is the shortest symbol a finding's text is searched for. A
// shorter one, such as Get or id, turns up in prose that is not about it.
const minSymbol = 5

// usedBy reports whether any finding relied on the entry: its ruling quoted
// a line of it, or its text names the entry's symbol.
func usedBy(e jev.Entry, fs []postmortem.Finding) bool {
	for _, f := range fs {
		if review.Quoted(f.Ruling.Evidence, e.Content) {
			return true
		}
		if len(e.Symbol) >= minSymbol && strings.Contains(f.Body, e.Symbol) {
			return true
		}
	}
	return false
}

func (tot *ContextTotals) tally(used []bool, scores []float64, sumUsed, sumOther *float64) {
	tot.Reviews++
	tot.Entries += len(scores)
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[order[a]] > scores[order[b]] })
	top := map[int]bool{}
	for _, i := range order[:(len(order)+1)/2] {
		top[i] = true
	}
	for i, u := range used {
		if !u {
			*sumOther += scores[i]
			continue
		}
		tot.Used++
		*sumUsed += scores[i]
		if top[i] {
			tot.KeptInTopHalf++
		}
		for j, v := range used {
			if v {
				continue
			}
			tot.Pairs++
			switch {
			case scores[i] > scores[j]:
				tot.Right++
			case scores[i] == scores[j]:
				tot.Right += 0.5
			}
		}
	}
}

// misses writes the misses replay's section of the report.
func (rep *Report) misses(b *strings.Builder) {
	tot := rep.Misses
	if tot == nil {
		return
	}
	b.WriteString("\nRecorded misses against the findings of their review\n")
	if len(tot.Seen) == 0 {
		b.WriteString("  No misses are recorded. `redline miss` records one.\n")
		return
	}
	count := map[string]int{}
	for _, s := range tot.Seen {
		count[s.Verdict]++
	}
	n := len(tot.Seen)
	fmt.Fprintf(b, "  %s recorded.\n", counted(n, "miss", "misses"))
	fmt.Fprintf(b, "    %s had no finding that described the defect\n", share(count[MissNotProposed], n))
	fmt.Fprintf(b, "    %s had a finding that described it and that the ruling withdrew\n", share(count[MissRuledOut], n))
	fmt.Fprintf(b, "    %s had a finding that described it and passed the ruling, so it was held at posting or not acted on\n",
		share(count[MissFound], n))
	if count[MissNoReview] > 0 {
		fmt.Fprintf(b, "    %s have no saved review of their head\n", share(count[MissNoReview], n))
	}
	for _, s := range tot.Seen {
		if s.Verdict == MissNoReview {
			continue
		}
		fmt.Fprintf(b, "  #%d %s:%d  %s\n      %s", s.PR, s.File, s.Line, clip(s.Summary, 90), s.Verdict)
		if s.Finding != "" {
			fmt.Fprintf(b, ": closest finding %s scored %.2f", s.Finding, s.Score)
			if s.Confidence != "" {
				fmt.Fprintf(b, ", confidence %s", s.Confidence)
			}
			fmt.Fprintf(b, "\n      %s", clip(s.Text, 110))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "  A finding describes a defect at a score of %.2f or more. That bar has not been set against\n"+
		"  labelled cases, so read the scores above before trusting the counts.\n", MissBar)
	if tot.Failed != "" {
		fmt.Fprintf(b, "  The replay failed on at least one miss: %s\n", tot.Failed)
	}
}

// evidence writes the evidence replay's section of the report.
func (rep *Report) evidence(b *strings.Builder) {
	tot := rep.Evidence
	if tot == nil {
		return
	}
	b.WriteString("\nWhether a ruling's quoted evidence shows what the finding claims\n")
	if len(tot.Seen) == 0 {
		b.WriteString("  No saved finding has a ruling that quoted evidence. Rulings come from review --verify.\n")
	} else {
		byVerdict := map[string][]float64{}
		for _, s := range tot.Seen {
			byVerdict[s.Verdict] = append(byVerdict[s.Verdict], s.Supports)
		}
		verdicts := make([]string, 0, len(byVerdict))
		for v := range byVerdict {
			verdicts = append(verdicts, v)
		}
		sort.Strings(verdicts)
		for _, v := range verdicts {
			ps := byVerdict[v]
			sort.Float64s(ps)
			low := sort.SearchFloat64s(ps, EvidenceBar)
			fmt.Fprintf(b, "  %-13s %s, median score %.2f, %s under %.2f\n",
				v, plural(len(ps), "finding"), ps[(len(ps)-1)/2], share(low, len(ps)), EvidenceBar)
		}
		var kept []EvidenceSeen
		for _, s := range tot.Seen {
			if s.Verdict == "kept" && s.Supports < EvidenceBar {
				kept = append(kept, s)
			}
		}
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].Supports < kept[j].Supports })
		if len(kept) > weakestShown {
			kept = kept[:weakestShown]
		}
		if len(kept) > 0 {
			b.WriteString("  Kept findings whose evidence scored lowest:\n")
		}
		for _, s := range kept {
			name := s.Session
			if len(s.Head) >= 7 {
				name += " " + s.Head[:7]
			}
			fmt.Fprintf(b, "    %s %s  %.2f\n      finding:  %s\n      evidence: %s\n",
				name, s.Finding, s.Supports, clip(s.Text, 100), clip(s.Evidence, 100))
		}
		b.WriteString("  A kept finding with a low score is one to read by hand: the ruling kept it on a line that\n" +
			"  may not show the problem.\n")
	}
	if tot.Failed != "" {
		fmt.Fprintf(b, "  The replay failed on at least one finding: %s\n", tot.Failed)
	}
}

// contextSection writes the context replay's section of the report.
func (rep *Report) contextSection(b *strings.Builder) {
	tot := rep.Context
	if tot == nil {
		return
	}
	b.WriteString("\nWhether a model can tell which context a review needs\n")
	if tot.Reviews == 0 {
		b.WriteString("  No saved review had both a context entry a finding used and one none did, so there was\n" +
			"  nothing to compare.\n")
	} else {
		fmt.Fprintf(b, "  %s had %s, and a finding used %d of them.\n",
			plural(tot.Reviews, "review"), counted(tot.Entries, "context entry", "context entries"), tot.Used)
		fmt.Fprintf(b, "    average score of a used entry       %.2f\n", tot.MeanUsed)
		fmt.Fprintf(b, "    average score of an entry not used  %.2f\n", tot.MeanOther)
		if tot.Pairs > 0 {
			fmt.Fprintf(b, "    a used entry scored above an unused one of the same review in %.0f%% of %s\n",
				100*tot.Right/float64(tot.Pairs), plural(tot.Pairs, "pair"))
		}
		fmt.Fprintf(b, "    keeping the higher-scoring half of each review's entries would have kept %s used entries\n",
			share(tot.KeptInTopHalf, tot.Used))
		b.WriteString("  Used means a ruling quoted the entry or a finding named its symbol. An entry a reviewer read\n" +
			"  and was reassured by is not counted, so 50% on the pairs line is what guessing would score.\n")
	}
	if tot.Skipped > 0 {
		fmt.Fprintf(b, "  %s with context were left out: every entry was used, or none was.\n", plural(tot.Skipped, "review"))
	}
	if tot.Failed != "" {
		fmt.Fprintf(b, "  The replay failed on at least one review: %s\n", tot.Failed)
	}
}

// counted is plural for a noun whose plural is not the noun with an s.
func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return comma(n) + " " + many
}

// clip cuts s to one line of at most n characters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
