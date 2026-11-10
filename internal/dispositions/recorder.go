package dispositions

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Recorder reads one PR end to end: GitHub for what was posted and resolved, git for what changed, and
// optional Redline artifacts for what GitHub does not hold.
type Recorder struct {
	GH  GitHub
	Git Runner
	Now func() time.Time
	// EvidenceMarker finds the PR comment that reports what the change was checked against. Empty skips it.
	EvidenceMarker string
}

// Record fetches the PR and its commits, then derives its rows. runs may be nil.
func (r Recorder) Record(ctx context.Context, number int, runs []Run) (Result, error) {
	pr, err := r.GH.FetchPR(ctx, number)
	if err != nil {
		return Result{}, err
	}
	// The commits of merged and force-pushed PRs are not on any local branch.
	if err := FetchPRCommits(ctx, r.Git, r.GH.Owner+"/"+r.GH.Name, strconv.Itoa(number)); err != nil {
		return Result{}, fmt.Errorf("fetch commits of PR %d: %w", number, err)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	res, err := Derive(pr, runs, NewGitDiffer(ctx, r.Git), now())
	if err != nil {
		return Result{}, err
	}
	RecordEvidence(&res.PR, pr.IssueComments, r.EvidenceMarker)
	return res, nil
}

// Summarize renders a plain-text summary of results: how many PRs, how many findings in each disposition,
// split by source, and evidence coverage. It is what the command prints, so a run is readable without
// opening the files.
func Summarize(results []Result) string {
	var b strings.Builder
	prs, withEvidence, bots := len(results), 0, 0
	disp := map[string]int{}
	bySource := map[string]map[string]int{}
	verified, fixed := 0, 0
	withReply, agree := 0, 0
	for i := range results {
		res := &results[i]
		if res.PR.HasEvidence == 1 {
			withEvidence++
		}
		if res.PR.AuthorKind == "bot" {
			bots++
		}
		for i := range res.Dispositions {
			d := &res.Dispositions[i]
			disp[d.Disposition]++
			src := d.Source
			if src == "" {
				src = "(no artifact)"
			}
			if bySource[src] == nil {
				bySource[src] = map[string]int{}
			}
			bySource[src][d.Disposition]++
			if d.ReplyKind != "" && d.GeometryDisposition != string(DispUnknown) {
				withReply++
				if (d.ReplyKind == ReplyAddressed) == (d.GeometryDisposition == string(DispFixed)) {
					agree++
				}
			}
			if d.Disposition == string(DispFixed) {
				fixed++
				verified += int(d.VerifiedAddressed)
			}
		}
	}
	fmt.Fprintf(&b, "PRs recorded: %d (bot-authored %d, with an evidence comment %d)\n", prs, bots, withEvidence)
	fmt.Fprintf(&b, "Findings by disposition: %s\n", counts(disp))
	for _, src := range sortedKeys(bySource) {
		fmt.Fprintf(&b, "  source %-14s %s\n", src, counts(bySource[src]))
	}
	closed := disp[string(DispFixed)] + disp[string(DispDeclined)]
	if closed > 0 {
		fmt.Fprintf(&b, "Resolved threads: %d fixed, %d declined (fix rate %.0f%%); fixed and verified addressed %d of %d\n",
			disp[string(DispFixed)], disp[string(DispDeclined)], 100*float64(disp[string(DispFixed)])/float64(closed), verified, fixed)
	}
	if withReply > 0 {
		fmt.Fprintf(&b, "Author reply vs anchor check: reply and geometry agree on %d of %d threads that have both\n", agree, withReply)
	}
	return b.String()
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
