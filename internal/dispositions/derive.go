package dispositions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/post"
)

// AnchorRadius is the ± window around a finding's line used to decide whether a later commit changed the
// code the finding was about. It is a starting default to tune on real data.
const AnchorRadius = 5

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func authorKind(bot bool) string {
	if bot {
		return "bot"
	}
	return "human"
}

// posted is a Redline finding found on GitHub, as a thread or in a review body.
type posted struct {
	markers  Markers
	thread   *Thread
	channel  string
	path     string
	severity string
	source   string // set for threads Redline did not open
}

// Derive turns a PR's GitHub state and any Redline run artifacts into rows. now stamps DerivedAt.
//
// GitHub is the source of truth for what was posted and what became of it: every posted finding carries
// markers that decode to its head commit, fingerprint and question. Artifacts add what GitHub does not hold
// (source, confidence, scope, findings that were never posted) and are optional.
func Derive(pr PR, runs []Run, diff Differ, now time.Time) (Result, error) {
	kind := authorKind(pr.AuthorIsBot)
	res := Result{PR: PRRow{
		PrNumber:   pr.Number,
		AuthorKind: kind,
		State:      pr.State,
		CreatedAt:  Ts(pr.CreatedAt),
		ClosedAt:   TsPtr(pr.ClosedAt),
		DerivedAt:  Ts(now),
	}}

	items, seenFP := collectPosted(pr, &res.PR)
	byFP := map[string]ArtifactFinding{}
	for i := range runs {
		fs := runs[i].Findings
		for j := range fs {
			byFP[fs[j].Fingerprint] = fs[j]
		}
	}
	// FindingKey groups re-asks of one question, but the tables keep one row per (PR, FindingKey, File), so a
	// second thread asking the same question on the same file is keyed by its own fingerprint instead.
	used := map[string]bool{}
	for i := range items {
		it := &items[i]
		af, hasArtifact := byFP[it.markers.Fingerprint]
		row, drow, err := buildRows(pr, *it, af, hasArtifact, runs, diff, kind, now)
		if err != nil {
			return Result{}, err
		}
		key := freeKey(used, drow.FindingKey, drow.FindingID, drow.File)
		row.FindingKey, drow.FindingKey = key, key
		used[rowKey(key, drow.File)] = true
		res.Findings = append(res.Findings, row)
		res.Dispositions = append(res.Dispositions, drow)
	}
	appendUnposted(&res, pr, runs, seenFP, used, kind, now)

	sort.SliceStable(res.Dispositions, func(i, j int) bool {
		a, b := res.Dispositions[i], res.Dispositions[j]
		return a.FindingKey+a.File < b.FindingKey+b.File
	})
	return res, nil
}

// RecordEvidence sets the PR's evidence columns from the comment that carries marker, if there is one. It is
// separate from Derive because the marker is the repository's own convention: a team that writes no evidence
// comment passes none, and the columns stay zero.
func RecordEvidence(row *PRRow, comments []string, marker string) {
	for _, body := range comments {
		if ev := ParseEvidence(body, marker); ev.Present {
			row.HasEvidence = 1
			row.EvidenceVerified = ev.Verified
			row.EvidenceRefuted = ev.Refuted
			row.EvidenceNotVerified = ev.NotVerified
		}
	}
}

// collectPosted lists every finding GitHub holds: one per review thread, then the findings folded into review
// bodies that no thread already carries. It counts threads and review runs on row as it goes.
func collectPosted(pr PR, row *PRRow) ([]posted, map[string]bool) {
	var items []posted
	seenFP := map[string]bool{}
	for i := range pr.Threads {
		th := &pr.Threads[i]
		if len(th.Comments) == 0 {
			continue
		}
		it := threadItem(pr.Commits, th)
		items = append(items, it)
		seenFP[it.markers.Fingerprint] = true
	}
	row.FindingsInThreads = len(items)
	for _, body := range pr.ReviewBodies {
		if ParseMarkers(body).ReviewHead != "" {
			row.ReviewRuns++
		}
		for _, m := range allFingerprints(body) {
			if !seenFP[m.Fingerprint] {
				items = append(items, posted{markers: m, channel: ChannelReviewBody})
				seenFP[m.Fingerprint] = true
			}
		}
	}
	return items, seenFP
}

// threadItem reads one review thread. A thread Redline did not open (Copilot, an earlier review bot, or a
// person) has no markers; escape review needs it too (two of the first six review near-misses were in such
// threads), so it gets a stable key of its own and is anchored to the commit that was the PR head when it was
// opened.
func threadItem(commits []Commit, th *Thread) posted {
	first := th.Comments[0]
	it := posted{markers: ParseMarkers(first.Body), thread: th, channel: ChannelThread, path: th.Path}
	if it.markers.Fingerprint != "" {
		// Only Redline's own "**Warning · agent**" prefix is a severity; other reviewers' first words are not.
		it.severity = severityOf(first.Body)
		return it
	}
	// The commit GitHub says the comment was made on. committedDate is when a commit was made, not pushed,
	// so headAt is only the fallback for comments GitHub has no commit for.
	head := first.CommitSHA
	if head == "" {
		head = headAt(commits, first.CreatedAt)
	}
	it.markers = Markers{
		HeadSHA:     head,
		Fingerprint: fmt.Sprintf("thread\x00%s\x00%d\x00%s", th.Path, th.OrigLine, first.CreatedAt.UTC().Format(time.RFC3339)),
	}
	it.source = SourceHumanReviewer
	if first.AuthorIsBot {
		it.source = SourceBotReviewer
	}
	return it
}

// appendUnposted adds the findings that appear only in artifacts: they were never posted anywhere.
// A question already posted under another fingerprint was asked, not dropped, so it gets no not-posted row
// (that row would replace the posted one).
func appendUnposted(res *Result, pr PR, runs []Run, seenFP, used map[string]bool, kind string, now time.Time) {
	for i := range runs {
		run := &runs[i]
		for j := range run.Findings {
			f := &run.Findings[j]
			if seenFP[f.Fingerprint] {
				continue
			}
			seenFP[f.Fingerprint] = true
			key, qid, fid := keysFor(f.QuestionKey, f.Fingerprint)
			if used[rowKey(key, f.File)] {
				continue
			}
			used[rowKey(key, f.File)] = true
			res.Findings = append(res.Findings, FindingRow{
				PrNumber: pr.Number, FindingKey: key, FindingID: fid, QuestionID: qid,
				Rule: f.Rule, Substrate: f.Substrate, Category: f.Category, Severity: f.Severity,
				Source: f.Source, Confidence: f.Confidence, VerifyVerdict: f.Verdict, File: f.File, Line: f.Line,
				Channel: ChannelArtifact, RunID: run.RunID, AuthorKind: kind, RecordedAt: Ts(now),
			})
			res.Dispositions = append(res.Dispositions, DispositionRow{
				PrNumber: pr.Number, FindingKey: key, File: f.File, QuestionID: qid, FindingID: fid,
				Disposition: string(DispNotPosted), AnchorKind: anchorKind(f.Line), Channel: ChannelArtifact,
				Severity: f.Severity, Source: f.Source, PrState: pr.State, AuthorKind: kind,
				LastActivityAt: Ts(run.ReviewedAt), DerivedAt: Ts(now),
			})
		}
	}
}

// headAt is the last commit made at or before t: the PR head a reviewer was looking at.
// Commits are in PR order. Empty when every commit is later than t.
func headAt(commits []Commit, t time.Time) string {
	head := ""
	for _, c := range commits {
		if c.Time.After(t) {
			break
		}
		head = c.SHA
	}
	return head
}

func rowKey(findingKey, file string) string { return findingKey + "\x00" + file }

// freeKey is key when no earlier row on file has it, else the finding id, else the finding id numbered by
// how many rows already took it (the same Redline finding posted as separate threads at two heads). Threads
// come from GitHub in creation order, so the numbering is the same on every derivation.
func freeKey(used map[string]bool, key, findingID, file string) string {
	if !used[rowKey(key, file)] {
		return key
	}
	if !used[rowKey(findingID, file)] {
		return findingID
	}
	for n := 2; ; n++ {
		if k := shortHash(fmt.Sprintf("%s\x00%d", findingID, n)); !used[rowKey(k, file)] {
			return k
		}
	}
}

func keysFor(questionKey, fingerprint string) (key, qid, fid string) {
	fid = shortHash(fingerprint)
	if questionKey != "" {
		qid = shortHash(questionKey)
		return qid, qid, fid
	}
	return fid, "", fid
}

func anchorKind(line int) string {
	if line > 0 {
		return AnchorLine
	}
	return AnchorFile
}

func allFingerprints(body string) []Markers {
	var out []Markers
	for _, f := range post.PostedFindings(body) {
		out = append(out, Markers{HeadSHA: f.Head, Fingerprint: f.Fingerprint})
	}
	return out
}

// severityOf reads the "**Warning · agent**" style prefix Redline writes on a posted comment.
func severityOf(body string) string {
	b := strings.TrimSpace(body)
	b = strings.TrimPrefix(b, "**")
	for _, sev := range []string{"error", "warning", "info"} {
		if strings.HasPrefix(strings.ToLower(b), sev) {
			return sev
		}
	}
	return ""
}

// details is what is known about one finding, from its thread and, when present, its artifact.
type details struct {
	qkey, file, sev, source, conf, verdict, rule, substrate, category string
	line, startLine                                                   int
	runID                                                             int64
}

func findingDetails(it posted, af ArtifactFinding, hasArtifact bool, runs []Run) details {
	d := details{qkey: it.markers.Question, file: it.path, sev: it.severity, source: it.source}
	if it.thread != nil {
		d.line = it.thread.OrigLine
		if d.line == 0 {
			d.line = it.thread.Line
		}
	}
	if !hasArtifact {
		return d
	}
	if d.qkey == "" {
		d.qkey = af.QuestionKey
	}
	if d.file == "" {
		d.file = af.File
	}
	if d.line == 0 {
		d.line = af.Line
	}
	d.startLine = af.StartLine
	if af.Severity != "" {
		d.sev = af.Severity
	}
	d.source, d.conf, d.verdict = af.Source, af.Confidence, af.Verdict
	d.rule, d.substrate, d.category = af.Rule, af.Substrate, af.Category
	for i := range runs {
		for j := range runs[i].Findings {
			if runs[i].Findings[j].Fingerprint == af.Fingerprint {
				d.runID = runs[i].RunID
			}
		}
	}
	return d
}

func buildRows(pr PR, it posted, af ArtifactFinding, hasArtifact bool, runs []Run, diff Differ, kind string, now time.Time) (FindingRow, DispositionRow, error) {
	d := findingDetails(it, af, hasArtifact, runs)
	key, qid, fid := keysFor(d.qkey, it.markers.Fingerprint)
	frow := FindingRow{
		PrNumber: pr.Number, HeadSha: it.markers.HeadSHA, FindingKey: key, FindingID: fid, QuestionID: qid,
		Rule: d.rule, Substrate: d.substrate, Category: d.category, Severity: d.sev, Source: d.source, Confidence: d.conf,
		VerifyVerdict: d.verdict, File: d.file, Line: d.line, Channel: it.channel, RunID: d.runID,
		AuthorKind: kind, RecordedAt: Ts(now),
	}
	drow := DispositionRow{
		PrNumber: pr.Number, FindingKey: key, File: d.file, QuestionID: qid, FindingID: fid,
		AnchorKind: anchorKind(d.line), Channel: it.channel, Severity: d.sev, Source: d.source,
		FirstHeadSha: it.markers.HeadSHA, PrState: pr.State, AuthorKind: kind, DerivedAt: Ts(now),
	}
	if it.thread == nil {
		// Folded into a review body: no thread, so no author action to read.
		drow.Disposition = string(DispNotPosted)
		return frow, drow, nil
	}
	th := it.thread
	drow.LastActivityAt = Ts(th.Comments[len(th.Comments)-1].CreatedAt)

	fix, err := findFix(pr, d.file, d.line, d.startLine, it.markers.HeadSHA, diff)
	if err != nil {
		return frow, drow, err
	}
	decideThread(&drow, pr.State, th, fix)
	if drow.Disposition == string(DispFixed) {
		fixedAt := commitTime(pr, drow.FixCommitSha)
		if drow.FixCommitSha == fix.sha {
			fixedAt = fix.time
		}
		if !fixedAt.IsZero() && verifiedAddressed(runs, d.file, d.qkey, fixedAt) {
			drow.VerifiedAddressed = 1
		}
	}
	return frow, drow, nil
}

// decideThread sets the disposition of a finding that has a thread. The author's reply decides a resolved
// thread; the anchor check (geometry) decides only when there is no recognisable reply, and is recorded either way.
// FixCommitSha is set only for a fixed finding: the commit the author cited, else the one geometry found.
func decideThread(drow *DispositionRow, state string, th *Thread, fix fixResult) {
	reply := readReply(th)
	drow.ReplyKind = reply.kind
	geometry := DispUnknown
	switch {
	case !fix.mapped:
	case fix.sha != "":
		geometry = DispFixed
	default:
		geometry = DispDeclined
	}
	drow.GeometryDisposition = string(geometry)

	ended := state == "MERGED" || state == "CLOSED"
	switch {
	case fix.deleted, !th.IsResolved && ended:
		drow.Disposition = string(DispStale)
	case !th.IsResolved:
		drow.Disposition = string(DispOpen)
	case reply.kind == ReplyAddressed:
		drow.Disposition, drow.Basis = string(DispFixed), BasisReply
		drow.FixCommitSha = reply.sha
		if drow.FixCommitSha == "" {
			drow.FixCommitSha = fix.sha
		}
	case reply.kind == ReplyNotFixing:
		drow.Disposition, drow.Basis = string(DispDeclined), BasisReply
	default:
		drow.Disposition, drow.Basis = string(geometry), BasisGeometry
		if geometry == DispFixed {
			drow.FixCommitSha = fix.sha
		}
	}
}

// verifiedAddressed reports whether a Redline run after the fix covered the file and did not raise the same
// question again. Without a question key there is nothing to compare, so it stays unverified.
func verifiedAddressed(runs []Run, file, qkey string, fixedAt time.Time) bool {
	if qkey == "" {
		return false
	}
	covered := false
	for i := range runs {
		r := &runs[i]
		if !r.ReviewedAt.After(fixedAt) {
			continue
		}
		if !slices.Contains(r.Scope, file) {
			continue
		}
		covered = true
		for j := range r.Findings {
			f := &r.Findings[j]
			if f.QuestionKey == qkey {
				return false
			}
		}
	}
	return covered
}

type reply struct {
	kind string
	sha  string
}

var (
	addressedRe = regexp.MustCompile(`(?i)\baddressed in\s+` + "`?" + `([0-9a-f]{7,40})`)
	notFixingRe = regexp.MustCompile(`(?i)\*\*not fixing\s*\(intentional\)`)
)

// readReply reads the author's disposition from the thread's replies, in the two reply formats this reads:
// "Addressed in <sha>: ..." and "**Not fixing (intentional).** ...". The last
// reply that says one of them wins, since a later reply can change the author's mind. The first comment is
// Redline's own and is never read.
func readReply(th *Thread) reply {
	var out reply
	for _, c := range th.Comments[1:] {
		if m := addressedRe.FindStringSubmatch(c.Body); m != nil {
			out = reply{kind: ReplyAddressed, sha: strings.ToLower(m[1])}
		} else if notFixingRe.MatchString(c.Body) {
			out = reply{kind: ReplyNotFixing}
		}
	}
	return out
}

// commitTime finds a commit by full SHA or by the abbreviation a reply carries.
func commitTime(pr PR, sha string) time.Time {
	if sha == "" {
		return time.Time{}
	}
	for _, c := range pr.Commits {
		if strings.HasPrefix(strings.ToLower(c.SHA), sha) {
			return c.Time
		}
	}
	return time.Time{}
}
