package dispositions

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

type fakeDiff struct {
	hunks   map[string][]Hunk
	deleted map[string]bool
	err     error
}

func (f fakeDiff) Hunks(commit, path string) ([]Hunk, bool, error) {
	k := commit + "|" + path
	return f.hunks[k], f.deleted[k], f.err
}

func commits(shas ...string) []Commit {
	out := make([]Commit, len(shas))
	for i, s := range shas {
		out[i] = Commit{SHA: s, Time: t0.Add(time.Duration(i) * time.Hour)}
	}
	return out
}

func edit(line, n int) []Hunk {
	return []Hunk{{OldStart: line, OldCount: n, NewStart: line, NewCount: n}}
}

// thread builds a Redline thread for a finding said at head.
func thread(line int, head, fingerprint, question string, resolved bool) Thread {
	body := "**Warning · agent** — something\n" + fpMarker(head, fingerprint)
	if question != "" {
		body += qMarker(question)
	}
	return Thread{
		Path: "a.go", Line: line, OrigLine: line, IsResolved: resolved,
		Comments: []ThreadComment{{Body: body, CreatedAt: t0}, {Body: "Addressed", CreatedAt: t0.Add(30 * time.Minute)}},
	}
}

func basePR(state string, ths ...Thread) PR {
	return PR{Number: 10, State: state, CreatedAt: t0, Commits: commits("c1", "c2", "c3"), Threads: ths}
}

func mustDerive(t *testing.T, pr PR, runs []Run, d Differ, now time.Time) Result {
	t.Helper()
	res, err := Derive(pr, runs, d, now)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return res
}

// derive1 returns the first thread disposition Derive produces.
func derive1(t *testing.T, pr PR, runs []Run, d fakeDiff) DispositionRow {
	t.Helper()
	res := mustDerive(t, pr, runs, d, t0.Add(48*time.Hour))
	for i := range res.Dispositions {
		if res.Dispositions[i].Channel == ChannelThread {
			return res.Dispositions[i]
		}
	}
	t.Fatal("no thread disposition derived")
	return DispositionRow{}
}

func byFile(rows []DispositionRow) map[string]DispositionRow {
	out := map[string]DispositionRow{}
	for _, d := range rows {
		out[d.File] = d
	}
	return out
}

func TestDerive_Dispositions(t *testing.T) {
	t.Parallel()
	q := "caller\x00foo"
	cases := []struct {
		name   string
		pr     PR
		d      fakeDiff
		want   Disposition
		fixSHA string
		anchor string
	}{
		{
			name: "resolved and a later commit edits the anchor is fixed",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d:    fakeDiff{hunks: map[string][]Hunk{"c2|a.go": edit(102, 1)}},
			want: DispFixed, fixSHA: "c2", anchor: AnchorLine,
		},
		{
			name: "resolved with edits far from the anchor is declined",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d:    fakeDiff{hunks: map[string][]Hunk{"c2|a.go": edit(400, 2), "c3|a.go": edit(1, 1)}},
			want: DispDeclined, anchor: AnchorLine,
		},
		{
			name: "an earlier insertion shifts the anchor so the later edit still lands on it",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d: fakeDiff{hunks: map[string][]Hunk{
				"c2|a.go": {{OldStart: 5, OldCount: 0, NewStart: 6, NewCount: 10}},
				"c3|a.go": edit(110, 1),
			}},
			want: DispFixed, fixSHA: "c3", anchor: AnchorLine,
		},
		{
			name: "an earlier insertion shifts the anchor so an edit at the old line number is not a fix",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d: fakeDiff{hunks: map[string][]Hunk{
				"c2|a.go": {{OldStart: 5, OldCount: 0, NewStart: 6, NewCount: 10}},
				"c3|a.go": edit(100, 1),
			}},
			want: DispDeclined, anchor: AnchorLine,
		},
		{
			name: "a merge from main shifts the anchor but is never the fix",
			pr: func() PR {
				p := basePR("MERGED", thread(100, "c1", "fp1", q, true))
				p.Commits[1].IsMerge = true
				return p
			}(),
			d:    fakeDiff{hunks: map[string][]Hunk{"c2|a.go": edit(100, 1)}},
			want: DispDeclined, anchor: AnchorLine,
		},
		{
			name: "unresolved on a merged PR is stale",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, false)),
			want: DispStale, anchor: AnchorLine,
		},
		{
			name: "unresolved on a closed PR is stale",
			pr:   basePR("CLOSED", thread(100, "c1", "fp1", q, false)),
			want: DispStale, anchor: AnchorLine,
		},
		{
			name: "unresolved on an open PR is open",
			pr:   basePR("OPEN", thread(100, "c1", "fp1", q, false)),
			want: DispOpen, anchor: AnchorLine,
		},
		{
			name: "a deleted file is stale even when the thread is resolved",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d:    fakeDiff{deleted: map[string]bool{"c2|a.go": true}},
			want: DispStale, anchor: AnchorLine,
		},
		{
			name: "resolved with the finding's commit missing from the PR is unknown",
			pr:   basePR("MERGED", thread(100, "dead0001", "fp1", q, true)),
			want: DispUnknown, anchor: AnchorLine,
		},
		{
			name: "a file-level finding is fixed by any later change to the file",
			pr:   basePR("MERGED", thread(0, "c1", "fp1", q, true)),
			d:    fakeDiff{hunks: map[string][]Hunk{"c3|a.go": edit(900, 1)}},
			want: DispFixed, fixSHA: "c3", anchor: AnchorFile,
		},
		{
			name: "a file-level finding with no later change is declined",
			pr:   basePR("MERGED", thread(0, "c1", "fp1", q, true)),
			want: DispDeclined, anchor: AnchorFile,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := derive1(t, tc.pr, nil, tc.d)
			if got.Disposition != string(tc.want) {
				t.Errorf("Disposition = %q, want %q", got.Disposition, tc.want)
			}
			if got.FixCommitSha != tc.fixSHA {
				t.Errorf("FixCommitSha = %q, want %q", got.FixCommitSha, tc.fixSHA)
			}
			if got.AnchorKind != tc.anchor {
				t.Errorf("AnchorKind = %q, want %q", got.AnchorKind, tc.anchor)
			}
			if got.FirstHeadSha == "" {
				t.Error("FirstHeadSha is empty")
			}
			if got.AuthorKind != "human" {
				t.Errorf("AuthorKind = %q, want human", got.AuthorKind)
			}
		})
	}
}

func TestDerive_FindingKeyUsesQuestionWhenPresent(t *testing.T) {
	t.Parallel()
	withQ := derive1(t, basePR("OPEN", thread(5, "c1", "fp1", "caller\x00foo", false)), nil, fakeDiff{})
	if withQ.QuestionID == "" || withQ.FindingKey != withQ.QuestionID {
		t.Errorf("with a question, FindingKey = %q, want the QuestionID %q", withQ.FindingKey, withQ.QuestionID)
	}
	if withQ.FindingKey == withQ.FindingID {
		t.Errorf("with a question, FindingKey should not be the FindingID %q", withQ.FindingID)
	}

	// No question falls back to the finding id, so findings do not collapse onto one empty key.
	noQ := derive1(t, basePR("OPEN", thread(5, "c1", "fp1", "", false)), nil, fakeDiff{})
	if noQ.QuestionID != "" || noQ.FindingKey != noQ.FindingID {
		t.Errorf("without a question, QuestionID = %q and FindingKey = %q, want none and the FindingID %q",
			noQ.QuestionID, noQ.FindingKey, noQ.FindingID)
	}
}

// The tables keep one row per (PR, FindingKey, File), so two rows with the same key would replace each other.
func TestDerive_FindingKeysAreUniquePerFile(t *testing.T) {
	t.Parallel()
	q := "caller\x00foo"
	pr := basePR("OPEN", thread(5, "c1", "fp1", q, false), thread(9, "c2", "fp2", q, false))
	runs := []Run{{PRNumber: 10, RunID: 1, ReviewedAt: t0, Findings: []ArtifactFinding{
		{Fingerprint: "fp3", File: "a.go", Line: 7, QuestionKey: q}, // the same question, re-asked and not posted
		{Fingerprint: "fp4", File: "b.go", Line: 7, QuestionKey: q}, // the same question on another file
	}}}
	res := mustDerive(t, pr, runs, fakeDiff{}, t0)
	seen := map[string]bool{}
	for _, d := range res.Dispositions {
		k := d.FindingKey + "\x00" + d.File
		if seen[k] {
			t.Errorf("duplicate key %q on %s", d.FindingKey, d.File)
		}
		seen[k] = true
	}
	if len(res.Dispositions) != 3 || len(res.Findings) != 3 {
		t.Fatalf("got %d dispositions and %d findings, want 3 each: a question already posted on the file gets no not-posted row",
			len(res.Dispositions), len(res.Findings))
	}
	var notPosted []string
	for _, d := range res.Dispositions {
		if d.Disposition == string(DispNotPosted) {
			notPosted = append(notPosted, d.File)
		}
	}
	if len(notPosted) != 1 || notPosted[0] != "b.go" {
		t.Errorf("not-posted rows on %q, want only b.go", notPosted)
	}
}

func TestDerive_SameFingerprintThreadsKeepTheirOwnRows(t *testing.T) {
	t.Parallel()
	// The same Redline finding, with no question, posted as a thread at three heads.
	pr := basePR("OPEN", thread(5, "c1", "fp1", "", false), thread(5, "c2", "fp1", "", false), thread(5, "c3", "fp1", "", false))
	first := mustDerive(t, pr, nil, fakeDiff{}, t0)
	keys := map[string]bool{}
	for _, d := range first.Dispositions {
		keys[d.FindingKey] = true
	}
	if len(keys) != 3 {
		t.Errorf("got %d distinct keys, want 3", len(keys))
	}
	again := mustDerive(t, pr, nil, fakeDiff{}, t0.Add(time.Hour))
	for i := range first.Findings {
		if first.Findings[i].FindingKey != again.Findings[i].FindingKey {
			t.Errorf("finding %d key changed across derivations: %q, then %q", i, first.Findings[i].FindingKey, again.Findings[i].FindingKey)
		}
	}
}

func TestDerive_ReviewBodyAndArtifactOnlyFindingsAreNotPosted(t *testing.T) {
	t.Parallel()
	pr := basePR("MERGED", thread(5, "c1", "fp1", "caller\x00foo", true))
	pr.ReviewBodies = []string{"summary\n" + fpMarker("c1", "folded-fp") + "\n<!-- redline:review:c1 -->"}
	runs := []Run{{PRNumber: 10, RunID: 77, ReviewedAt: t0, Scope: []string{"a.go"}, Findings: []ArtifactFinding{
		{Fingerprint: "fp1", File: "a.go", Line: 5, Source: "llm", Confidence: "high", Severity: "warning", Rule: "agent-comment", Category: "review", Substrate: "redline/review"},
		{Fingerprint: "only-in-artifact", File: "b.go", Source: "deterministic", Severity: "info", Rule: "assertions-removed", Category: "tests", Substrate: "redline/test-delta"},
	}}}
	res := mustDerive(t, pr, runs, fakeDiff{}, t0)
	if res.PR.ReviewRuns != 1 || res.PR.FindingsInThreads != 1 {
		t.Errorf("ReviewRuns, FindingsInThreads = %d, %d, want 1, 1", res.PR.ReviewRuns, res.PR.FindingsInThreads)
	}

	byChannel := map[string]DispositionRow{}
	for _, d := range res.Dispositions {
		byChannel[d.Channel] = d
	}
	if len(res.Dispositions) != 3 {
		t.Fatalf("got %d dispositions, want 3", len(res.Dispositions))
	}
	for _, ch := range []string{ChannelReviewBody, ChannelArtifact} {
		if got := byChannel[ch].Disposition; got != string(DispNotPosted) {
			t.Errorf("%s disposition = %q, want %q", ch, got, DispNotPosted)
		}
	}
	art := byChannel[ChannelArtifact]
	if art.AnchorKind != AnchorFile {
		t.Errorf("an artifact finding with no line has AnchorKind %q, want %q", art.AnchorKind, AnchorFile)
	}
	if art.Source != "deterministic" {
		t.Errorf("artifact Source = %q, want deterministic", art.Source)
	}

	// The posted finding picks up what only the artifact knows.
	var posted FindingRow
	for _, f := range res.Findings {
		if f.Channel == ChannelThread {
			posted = f
		}
	}
	if posted.Source != "llm" || posted.Confidence != "high" || posted.RunID != 77 || posted.Substrate != "redline/review" {
		t.Errorf("posted finding = source %q, confidence %q, run %d, substrate %q; want llm, high, 77, redline/review",
			posted.Source, posted.Confidence, posted.RunID, posted.Substrate)
	}
}

func TestDerive_VerifiedAddressed(t *testing.T) {
	t.Parallel()
	q := "caller\x00foo"
	pr := basePR("MERGED", thread(100, "c1", "fp1", q, true))
	d := fakeDiff{hunks: map[string][]Hunk{"c2|a.go": edit(100, 1)}}
	fixedAt := t0.Add(time.Hour) // c2's time

	later := func(scope []string, qk string) []Run {
		return []Run{{PRNumber: 10, ReviewedAt: fixedAt.Add(time.Hour), Scope: scope, Findings: []ArtifactFinding{
			{Fingerprint: "other", File: "a.go", QuestionKey: qk},
		}}}
	}
	cases := []struct {
		name string
		pr   PR
		runs []Run
		want uint8
	}{
		{"a later run covers the file and does not raise the question", pr, later([]string{"a.go"}, ""), 1},
		{"a later run raises the same question again", pr, later([]string{"a.go"}, q), 0},
		{"a later run that did not cover the file proves nothing", pr, later([]string{"z.go"}, ""), 0},
		{"no later run at all", pr, nil, 0},
		{"a run from before the fix does not count", pr, []Run{{ReviewedAt: t0, Scope: []string{"a.go"}}}, 0},
		{"without a question key there is nothing to compare", basePR("MERGED", thread(100, "c1", "fp1", "", true)),
			later([]string{"a.go"}, ""), 0},
	}
	for _, tc := range cases {
		got := derive1(t, tc.pr, tc.runs, d)
		if got.Disposition != string(DispFixed) {
			t.Errorf("%s: Disposition = %q, want fixed", tc.name, got.Disposition)
		}
		if got.VerifiedAddressed != tc.want {
			t.Errorf("%s: VerifiedAddressed = %d, want %d", tc.name, got.VerifiedAddressed, tc.want)
		}
	}
}

func TestDerive_PRRowAndEvidence(t *testing.T) {
	t.Parallel()
	pr := basePR("MERGED")
	pr.AuthorIsBot = true
	pr.ClosedAt = t0.Add(5 * time.Hour)
	pr.IssueComments = []string{"unrelated", evidenceBody}
	res := mustDerive(t, pr, nil, fakeDiff{}, t0)
	if res.PR.HasEvidence != 0 {
		t.Error("Derive should leave evidence to RecordEvidence")
	}
	RecordEvidence(&res.PR, pr.IssueComments, testMarker)
	row := res.PR
	if row.AuthorKind != "bot" || row.State != "MERGED" {
		t.Errorf("AuthorKind, State = %q, %q, want bot, MERGED", row.AuthorKind, row.State)
	}
	if row.HasEvidence != 1 || row.EvidenceVerified != 2 || row.EvidenceRefuted != 1 || row.EvidenceNotVerified != 2 {
		t.Errorf("evidence = has %d, verified %d, refuted %d, not verified %d; want 1, 2, 1, 2",
			row.HasEvidence, row.EvidenceVerified, row.EvidenceRefuted, row.EvidenceNotVerified)
	}
	if row.ClosedAt == nil {
		t.Error("a merged PR should have a closed time")
	}

	open := mustDerive(t, basePR("OPEN"), nil, fakeDiff{}, t0)
	if open.PR.HasEvidence != 0 {
		t.Error("a PR without an evidence comment has no evidence")
	}
	if open.PR.ClosedAt != nil {
		t.Errorf("an open PR has no closed time, got %v", open.PR.ClosedAt)
	}
}

func TestDerive_DiffErrorPropagates(t *testing.T) {
	t.Parallel()
	pr := basePR("MERGED", thread(100, "c1", "fp1", "", true))
	if _, err := Derive(pr, nil, fakeDiff{err: errors.New("boom")}, t0); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Derive error = %v, want the diff's error", err)
	}
}

func TestDerive_ThreadsRedlineDidNotOpen(t *testing.T) {
	t.Parallel()
	bot := Thread{Path: "a.go", OrigLine: 50, IsResolved: true, Comments: []ThreadComment{
		{Body: "Email is not a unique key.", CreatedAt: t0.Add(30 * time.Minute), AuthorIsBot: true},
		{Body: "**Not fixing (intentional).** Separate hardening change.", CreatedAt: t0.Add(40 * time.Minute)},
	}}
	human := Thread{Path: "b.go", OrigLine: 10, IsResolved: true, Comments: []ThreadComment{
		{Body: "this drops the zone", CreatedAt: t0.Add(90 * time.Minute)},
	}}
	early := Thread{Path: "c.go", OrigLine: 1, IsResolved: true, Comments: []ThreadComment{
		{Body: "Error handling here looks fine. Before any commit.", CreatedAt: t0.Add(-time.Hour)},
	}}
	// Made on c1 but posted after c2 was committed (c2 was pushed later): GitHub's commit wins over the clock.
	pushedLate := Thread{Path: "e.go", OrigLine: 3, IsResolved: true, Comments: []ThreadComment{
		{Body: "Warnings are swallowed", CreatedAt: t0.Add(90 * time.Minute), CommitSHA: "c1"},
	}}
	empty := Thread{Path: "d.go"}
	pr := basePR("MERGED", bot, human, early, pushedLate, empty)
	diff := fakeDiff{hunks: map[string][]Hunk{"c3|b.go": edit(10, 1)}}
	res := mustDerive(t, pr, nil, diff, t0.Add(48*time.Hour))
	if len(res.Dispositions) != 4 || res.PR.FindingsInThreads != 4 {
		t.Fatalf("got %d dispositions and %d thread findings, want 4 each: a thread with no comments is skipped",
			len(res.Dispositions), res.PR.FindingsInThreads)
	}

	rows := byFile(res.Dispositions)
	cases := []struct {
		file, source, disposition, head, why string
	}{
		{"a.go", SourceBotReviewer, string(DispDeclined), "c1", "the author's reply decides, and the head is the PR's when the thread was opened"},
		{"b.go", SourceHumanReviewer, string(DispFixed), "c2", "no reply, so geometry: c3 changed the anchor"},
		{"c.go", "", string(DispUnknown), "", "opened before any commit: no head to diff from, no reply"},
		{"e.go", "", "", "c1", "GitHub's commit wins over the clock"},
	}
	for _, tc := range cases {
		got := rows[tc.file]
		if tc.source != "" && got.Source != tc.source {
			t.Errorf("%s: Source = %q, want %q", tc.file, got.Source, tc.source)
		}
		if tc.disposition != "" && got.Disposition != tc.disposition {
			t.Errorf("%s: Disposition = %q, want %q (%s)", tc.file, got.Disposition, tc.disposition, tc.why)
		}
		if got.FirstHeadSha != tc.head {
			t.Errorf("%s: FirstHeadSha = %q, want %q (%s)", tc.file, got.FirstHeadSha, tc.head, tc.why)
		}
	}
	for _, f := range []string{"c.go", "e.go"} {
		if sev := rows[f].Severity; sev != "" {
			t.Errorf("%s: Severity = %q, want none: a reviewer's first word is not a severity", f, sev)
		}
	}
	if rows["a.go"].FindingKey == rows["b.go"].FindingKey {
		t.Errorf("two threads share the key %q", rows["a.go"].FindingKey)
	}
	again := mustDerive(t, pr, nil, diff, t0.Add(72*time.Hour))
	if res.Dispositions[0].FindingKey != again.Dispositions[0].FindingKey {
		t.Errorf("the key changed across derivations: %q, then %q", res.Dispositions[0].FindingKey, again.Dispositions[0].FindingKey)
	}
}

func TestHeadAt(t *testing.T) {
	t.Parallel()
	cs := commits("c1", "c2", "c3")
	cases := []struct {
		name string
		cs   []Commit
		at   time.Time
		want string
	}{
		{"at the first commit", cs, t0, "c1"},
		{"between commits", cs, t0.Add(90 * time.Minute), "c2"},
		{"after the last", cs, t0.Add(10 * time.Hour), "c3"},
		{"before any commit", cs, t0.Add(-time.Second), ""},
		{"no commits", nil, t0, ""},
	}
	for _, tc := range cases {
		if got := headAt(tc.cs, tc.at); got != tc.want {
			t.Errorf("%s: headAt = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSeverityOf(t *testing.T) {
	t.Parallel()
	for body, want := range map[string]string{
		"**Warning · agent** — x": "warning",
		"**Error · agent** — x":   "error",
		"**Info · agent** — x":    "info",
		"no severity":             "",
	} {
		if got := severityOf(body); got != want {
			t.Errorf("severityOf(%q) = %q, want %q", body, got, want)
		}
	}
}

func basePRLong(state string, ths ...Thread) PR {
	pr := basePR(state, ths...)
	pr.Commits = commits("c1", "c2c2c2c2", "c3c3c3c3")
	return pr
}

func withReplies(th Thread, replies ...string) Thread {
	for i, r := range replies {
		th.Comments = append(th.Comments, ThreadComment{Body: r, CreatedAt: t0.Add(time.Duration(i+1) * time.Hour)})
	}
	return th
}

func TestDerive_AuthorReplyIsThePrimarySignal(t *testing.T) {
	t.Parallel()
	q := "caller\x00foo"
	bare := func(resolved bool) Thread {
		th := thread(100, "c1", "fp1", q, resolved)
		th.Comments = th.Comments[:1]
		return th
	}
	farEdit := fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(400, 1)}}
	onAnchor := fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(100, 1)}}

	t.Run("an Addressed reply wins over geometry that sees no change near the anchor", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "Addressed in c3c3c3c3: added a row elsewhere in the file")), nil, farEdit)
		if got.Disposition != string(DispFixed) || got.Basis != BasisReply || got.ReplyKind != ReplyAddressed {
			t.Errorf("Disposition, Basis, ReplyKind = %q, %q, %q, want fixed, reply, addressed", got.Disposition, got.Basis, got.ReplyKind)
		}
		if got.GeometryDisposition != string(DispDeclined) {
			t.Errorf("GeometryDisposition = %q, want declined: the disagreement is recorded", got.GeometryDisposition)
		}
		if got.FixCommitSha != "c3c3c3c3" {
			t.Errorf("FixCommitSha = %q, want c3c3c3c3, the sha the author named", got.FixCommitSha)
		}
	})
	t.Run("a Not fixing reply wins over geometry that sees a change", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "**Not fixing (intentional).** Same on main.")), nil, onAnchor)
		if got.Disposition != string(DispDeclined) || got.Basis != BasisReply || got.ReplyKind != ReplyNotFixing {
			t.Errorf("Disposition, Basis, ReplyKind = %q, %q, %q, want declined, reply, not-fixing", got.Disposition, got.Basis, got.ReplyKind)
		}
		if got.GeometryDisposition != string(DispFixed) {
			t.Errorf("GeometryDisposition = %q, want fixed", got.GeometryDisposition)
		}
		if got.FixCommitSha != "" {
			t.Errorf("FixCommitSha = %q, want none: a declined finding has no fix commit, whatever else touched the anchor", got.FixCommitSha)
		}
	})
	t.Run("the commit the author cites wins over the one geometry found", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "Addressed in c3c3c3c3: the real fix")), nil, onAnchor)
		if got.FixCommitSha != "c3c3c3c3" {
			t.Errorf("FixCommitSha = %q, want c3c3c3c3", got.FixCommitSha)
		}
	})
	t.Run("the last reply that states a disposition wins", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "**Not fixing (intentional).** no", "thanks", "Addressed in c2c2c2c2: changed my mind")), nil, onAnchor)
		if got.Disposition != string(DispFixed) || got.FixCommitSha != "c2c2c2c2" {
			t.Errorf("Disposition, FixCommitSha = %q, %q, want fixed, c2c2c2c2", got.Disposition, got.FixCommitSha)
		}
	})
	t.Run("with no recognisable reply geometry decides", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "done, thanks")), nil, onAnchor)
		if got.Disposition != string(DispFixed) || got.Basis != BasisGeometry || got.ReplyKind != "" {
			t.Errorf("Disposition, Basis, ReplyKind = %q, %q, %q, want fixed, geometry, none", got.Disposition, got.Basis, got.ReplyKind)
		}
	})
	t.Run("a reply does not resolve a thread that is still unresolved", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("OPEN", withReplies(bare(false), "Addressed in c2c2c2c2: done")), nil, onAnchor)
		if got.Disposition != string(DispOpen) || got.Basis != "" {
			t.Errorf("Disposition, Basis = %q, %q, want open, none", got.Disposition, got.Basis)
		}
	})
	t.Run("an Addressed reply rescues a resolved thread whose commit left the PR", func(t *testing.T) {
		t.Parallel()
		th := withReplies(thread(100, "dead0001", "fp1", q, true), "Addressed in c2c2c2c2: done")
		got := derive1(t, basePRLong("MERGED", th), nil, fakeDiff{})
		if got.GeometryDisposition != string(DispUnknown) || got.Disposition != string(DispFixed) {
			t.Errorf("GeometryDisposition, Disposition = %q, %q, want unknown, fixed", got.GeometryDisposition, got.Disposition)
		}
	})
	t.Run("Redline's own first comment is never read as a reply", func(t *testing.T) {
		t.Parallel()
		th := thread(100, "c1", "fp1", q, true)
		th.Comments = th.Comments[:1]
		th.Comments[0].Body += "\nAddressed in c2c2c2c2"
		if got := derive1(t, basePRLong("MERGED", th), nil, farEdit); got.ReplyKind != "" {
			t.Errorf("ReplyKind = %q, want none", got.ReplyKind)
		}
	})
	t.Run("an Addressed reply feeds verified-addressed through the commit it names", func(t *testing.T) {
		t.Parallel()
		runs := []Run{{ReviewedAt: t0.Add(10 * time.Hour), Scope: []string{"a.go"}}}
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "Addressed in c2c2c2c2: done")), runs, farEdit)
		if got.VerifiedAddressed != 1 {
			t.Errorf("VerifiedAddressed = %d, want 1", got.VerifiedAddressed)
		}
	})
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	pr := basePRLong("MERGED",
		withReplies(thread(100, "c1", "fp1", "caller\x00foo", true), "Addressed in c2c2c2c2: done"),
		withReplies(thread(300, "c1", "fp2", "caller\x00bar", true), "Addressed in c3c3c3c3: elsewhere"),
	)
	pr.IssueComments = []string{evidenceBody}
	res := mustDerive(t, pr, nil, fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(100, 1)}}, t0)
	RecordEvidence(&res.PR, pr.IssueComments, testMarker)
	out := Summarize([]Result{res})
	for _, want := range []string{
		"PRs recorded: 1 (bot-authored 0, with an evidence comment 1)",
		"fixed=2",
		"reply and geometry agree on 1 of 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
	if want := "PRs recorded: 0 (bot-authored 0, with an evidence comment 0)\nFindings by disposition: none\n"; Summarize(nil) != want {
		t.Errorf("empty summary = %q, want %q", Summarize(nil), want)
	}
}
