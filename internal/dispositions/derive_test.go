package dispositions

import (
	"errors"
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

func derive1(t *testing.T, pr PR, runs []Run, d fakeDiff) DispositionRow {
	t.Helper()
	res, err := Derive(pr, runs, d, t0.Add(48*time.Hour))
	require.NoError(t, err)
	for i := range res.Dispositions {
		if res.Dispositions[i].Channel == ChannelThread {
			return res.Dispositions[i]
		}
	}
	require.Fail(t, "no thread disposition derived")
	return DispositionRow{}
}

func TestDerive_Dispositions(t *testing.T) {
	t.Parallel()
	q := "caller\x00foo"
	cases := []struct {
		name    string
		pr      PR
		d       fakeDiff
		want    Disposition
		fixSHA  string
		anchor  string
		channel string
	}{
		{
			name: "resolved and a later commit edits the anchor is fixed",
			pr:   basePR("MERGED", thread(100, "c1", "fp1", q, true)),
			d:    fakeDiff{hunks: map[string][]Hunk{"c2|a.go": edit(102, 1)}},
			want: DispFixed, fixSHA: "c2", anchor: AnchorLine, channel: ChannelThread,
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
			assert.Equal(t, string(tc.want), got.Disposition)
			assert.Equal(t, tc.fixSHA, got.FixCommitSha)
			assert.Equal(t, tc.anchor, got.AnchorKind)
			assert.NotEmpty(t, got.FirstHeadSha)
			assert.Equal(t, "human", got.AuthorKind)
		})
	}
}

func TestDerive_FindingKeyUsesQuestionWhenPresent(t *testing.T) {
	t.Parallel()
	withQ := derive1(t, basePR("OPEN", thread(5, "c1", "fp1", "caller\x00foo", false)), nil, fakeDiff{})
	assert.NotEmpty(t, withQ.QuestionID)
	assert.Equal(t, withQ.QuestionID, withQ.FindingKey)
	assert.NotEqual(t, withQ.FindingID, withQ.FindingKey)

	noQ := derive1(t, basePR("OPEN", thread(5, "c1", "fp1", "", false)), nil, fakeDiff{})
	assert.Empty(t, noQ.QuestionID)
	assert.Equal(t, noQ.FindingID, noQ.FindingKey, "no question falls back to the finding id, so findings do not collapse onto one empty key")
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
	res, err := Derive(pr, runs, fakeDiff{}, t0)
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, d := range res.Dispositions {
		k := d.FindingKey + "\x00" + d.File
		assert.False(t, seen[k], "duplicate key %q on %s", d.FindingKey, d.File)
		seen[k] = true
	}
	require.Len(t, res.Dispositions, 3, "a question already posted on the file gets no not-posted row")
	require.Len(t, res.Findings, 3)
	var notPosted []DispositionRow
	for _, d := range res.Dispositions {
		if d.Disposition == string(DispNotPosted) {
			notPosted = append(notPosted, d)
		}
	}
	require.Len(t, notPosted, 1)
	assert.Equal(t, "b.go", notPosted[0].File)
}

func TestDerive_SameFingerprintThreadsKeepTheirOwnRows(t *testing.T) {
	t.Parallel()
	// The same Redline finding, with no question, posted as a thread at three heads.
	pr := basePR("OPEN", thread(5, "c1", "fp1", "", false), thread(5, "c2", "fp1", "", false), thread(5, "c3", "fp1", "", false))
	first, err := Derive(pr, nil, fakeDiff{}, t0)
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, d := range first.Dispositions {
		keys[d.FindingKey] = true
	}
	assert.Len(t, keys, 3)
	again, err := Derive(pr, nil, fakeDiff{}, t0.Add(time.Hour))
	require.NoError(t, err)
	for i := range first.Findings {
		assert.Equal(t, first.Findings[i].FindingKey, again.Findings[i].FindingKey, "stable across derivations")
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
	res, err := Derive(pr, runs, fakeDiff{}, t0)
	require.NoError(t, err)
	assert.Equal(t, 1, res.PR.ReviewRuns)
	assert.Equal(t, 1, res.PR.FindingsInThreads)

	byChannel := map[string]DispositionRow{}
	for _, d := range res.Dispositions {
		byChannel[d.Channel] = d
	}
	require.Len(t, res.Dispositions, 3)
	assert.Equal(t, string(DispNotPosted), byChannel[ChannelReviewBody].Disposition)
	assert.Equal(t, string(DispNotPosted), byChannel[ChannelArtifact].Disposition)
	assert.Equal(t, AnchorFile, byChannel[ChannelArtifact].AnchorKind, "no line means a file-level anchor")
	assert.Equal(t, "deterministic", byChannel[ChannelArtifact].Source)

	// The posted finding picks up what only the artifact knows.
	var posted FindingRow
	for _, f := range res.Findings {
		if f.Channel == ChannelThread {
			posted = f
		}
	}
	assert.Equal(t, "llm", posted.Source)
	assert.Equal(t, "high", posted.Confidence)
	assert.Equal(t, int64(77), posted.RunID)
	assert.Equal(t, "redline/review", posted.Substrate)
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
		runs []Run
		want uint8
	}{
		{"a later run covers the file and does not raise the question", later([]string{"a.go"}, ""), 1},
		{"a later run raises the same question again", later([]string{"a.go"}, q), 0},
		{"a later run that did not cover the file proves nothing", later([]string{"z.go"}, ""), 0},
		{"no later run at all", nil, 0},
		{"a run from before the fix does not count", []Run{{ReviewedAt: t0, Scope: []string{"a.go"}}}, 0},
	}
	for _, tc := range cases {
		got := derive1(t, pr, tc.runs, d)
		assert.Equal(t, string(DispFixed), got.Disposition, tc.name)
		assert.Equal(t, tc.want, got.VerifiedAddressed, tc.name)
	}

	// Without a question key there is nothing to compare, so a fix stays unverified.
	noQ := basePR("MERGED", thread(100, "c1", "fp1", "", true))
	got := derive1(t, noQ, later([]string{"a.go"}, ""), d)
	assert.Equal(t, string(DispFixed), got.Disposition)
	assert.Zero(t, got.VerifiedAddressed)
}

func TestDerive_PRRowAndEvidence(t *testing.T) {
	t.Parallel()
	pr := basePR("MERGED")
	pr.AuthorIsBot = true
	pr.ClosedAt = t0.Add(5 * time.Hour)
	pr.IssueComments = []string{"unrelated", evidenceBody}
	res, err := Derive(pr, nil, fakeDiff{}, t0)
	require.NoError(t, err)
	assert.Zero(t, res.PR.HasEvidence, "Derive leaves evidence to RecordEvidence")
	RecordEvidence(&res.PR, pr.IssueComments, testMarker)
	assert.Equal(t, "bot", res.PR.AuthorKind)
	assert.Equal(t, "MERGED", res.PR.State)
	assert.Equal(t, uint8(1), res.PR.HasEvidence)
	assert.Equal(t, 2, res.PR.EvidenceVerified)
	assert.Equal(t, 1, res.PR.EvidenceRefuted)
	assert.Equal(t, 2, res.PR.EvidenceNotVerified)
	require.NotNil(t, res.PR.ClosedAt)

	open, err := Derive(basePR("OPEN"), nil, fakeDiff{}, t0)
	require.NoError(t, err)
	assert.Zero(t, open.PR.HasEvidence)
	assert.Nil(t, open.PR.ClosedAt, "an open PR has no closed time")
}

func TestDerive_DiffErrorPropagates(t *testing.T) {
	t.Parallel()
	pr := basePR("MERGED", thread(100, "c1", "fp1", "", true))
	_, err := Derive(pr, nil, fakeDiff{err: errors.New("boom")}, t0)
	require.ErrorContains(t, err, "boom")
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
	res, err := Derive(pr, nil, fakeDiff{hunks: map[string][]Hunk{"c3|b.go": edit(10, 1)}}, t0.Add(48*time.Hour))
	require.NoError(t, err)
	require.Len(t, res.Dispositions, 4, "a thread with no comments is skipped")
	assert.Equal(t, 4, res.PR.FindingsInThreads)

	byFile := map[string]DispositionRow{}
	for _, d := range res.Dispositions {
		byFile[d.File] = d
	}
	assert.Equal(t, SourceBotReviewer, byFile["a.go"].Source)
	assert.Equal(t, string(DispDeclined), byFile["a.go"].Disposition, "the author's reply decides, as for Redline threads")
	assert.Equal(t, "c1", byFile["a.go"].FirstHeadSha, "anchored to the PR head when the thread was opened")

	assert.Equal(t, SourceHumanReviewer, byFile["b.go"].Source)
	assert.Equal(t, "c2", byFile["b.go"].FirstHeadSha)
	assert.Equal(t, string(DispFixed), byFile["b.go"].Disposition, "geometry: c3 changed the anchor")

	assert.Empty(t, byFile["c.go"].FirstHeadSha)
	assert.Equal(t, string(DispUnknown), byFile["c.go"].Disposition, "no head to diff from, no reply")
	assert.Empty(t, byFile["c.go"].Severity, "a reviewer's first word is not a severity")
	assert.Equal(t, "c1", byFile["e.go"].FirstHeadSha)
	assert.Empty(t, byFile["e.go"].Severity)

	assert.NotEqual(t, byFile["a.go"].FindingKey, byFile["b.go"].FindingKey)
	again, err := Derive(pr, nil, fakeDiff{hunks: map[string][]Hunk{"c3|b.go": edit(10, 1)}}, t0.Add(72*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, res.Dispositions[0].FindingKey, again.Dispositions[0].FindingKey, "the key is stable across derivations")
}

func TestHeadAt(t *testing.T) {
	t.Parallel()
	cs := commits("c1", "c2", "c3")
	assert.Equal(t, "c1", headAt(cs, t0))
	assert.Equal(t, "c2", headAt(cs, t0.Add(90*time.Minute)))
	assert.Equal(t, "c3", headAt(cs, t0.Add(10*time.Hour)))
	assert.Empty(t, headAt(cs, t0.Add(-time.Second)))
	assert.Empty(t, headAt(nil, t0))
}

func TestSeverityOf(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "warning", severityOf("**Warning · agent** — x"))
	assert.Equal(t, "error", severityOf("**Error · agent** — x"))
	assert.Equal(t, "info", severityOf("**Info · agent** — x"))
	assert.Empty(t, severityOf("no severity"))
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
		assert.Equal(t, string(DispFixed), got.Disposition)
		assert.Equal(t, BasisReply, got.Basis)
		assert.Equal(t, ReplyAddressed, got.ReplyKind)
		assert.Equal(t, string(DispDeclined), got.GeometryDisposition, "geometry disagrees, and that is recorded")
		assert.Equal(t, "c3c3c3c3", got.FixCommitSha, "the sha the author named")
	})
	t.Run("a Not fixing reply wins over geometry that sees a change", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "**Not fixing (intentional).** Same on main.")), nil, onAnchor)
		assert.Equal(t, string(DispDeclined), got.Disposition)
		assert.Equal(t, BasisReply, got.Basis)
		assert.Equal(t, ReplyNotFixing, got.ReplyKind)
		assert.Equal(t, string(DispFixed), got.GeometryDisposition)
		assert.Empty(t, got.FixCommitSha, "a declined finding has no fix commit, whatever else touched the anchor")
	})
	t.Run("the commit the author cites wins over the one geometry found", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "Addressed in c3c3c3c3: the real fix")), nil, onAnchor)
		assert.Equal(t, "c3c3c3c3", got.FixCommitSha)
	})
	t.Run("the last reply that states a disposition wins", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "**Not fixing (intentional).** no", "thanks", "Addressed in c2c2c2c2: changed my mind")), nil, onAnchor)
		assert.Equal(t, string(DispFixed), got.Disposition)
		assert.Equal(t, "c2c2c2c2", got.FixCommitSha, "the commit the author named")
	})
	t.Run("with no recognisable reply geometry decides", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "done, thanks")), nil, onAnchor)
		assert.Equal(t, string(DispFixed), got.Disposition)
		assert.Equal(t, BasisGeometry, got.Basis)
		assert.Empty(t, got.ReplyKind)
	})
	t.Run("a reply does not resolve a thread that is still unresolved", func(t *testing.T) {
		t.Parallel()
		got := derive1(t, basePRLong("OPEN", withReplies(bare(false), "Addressed in c2c2c2c2: done")), nil, onAnchor)
		assert.Equal(t, string(DispOpen), got.Disposition)
		assert.Empty(t, got.Basis)
	})
	t.Run("an Addressed reply rescues a resolved thread whose commit left the PR", func(t *testing.T) {
		t.Parallel()
		th := withReplies(thread(100, "dead0001", "fp1", q, true), "Addressed in c2c2c2c2: done")
		got := derive1(t, basePRLong("MERGED", th), nil, fakeDiff{})
		assert.Equal(t, string(DispUnknown), got.GeometryDisposition)
		assert.Equal(t, string(DispFixed), got.Disposition)
	})
	t.Run("Redline's own first comment is never read as a reply", func(t *testing.T) {
		t.Parallel()
		th := thread(100, "c1", "fp1", q, true)
		th.Comments = th.Comments[:1]
		th.Comments[0].Body += "\nAddressed in c2c2c2c2"
		got := derive1(t, basePRLong("MERGED", th), nil, farEdit)
		assert.Empty(t, got.ReplyKind)
	})
	t.Run("an Addressed reply feeds verified-addressed through the commit it names", func(t *testing.T) {
		t.Parallel()
		runs := []Run{{ReviewedAt: t0.Add(10 * time.Hour), Scope: []string{"a.go"}}}
		got := derive1(t, basePRLong("MERGED", withReplies(bare(true), "Addressed in c2c2c2c2: done")), runs, farEdit)
		assert.Equal(t, uint8(1), got.VerifiedAddressed)
	})
}

func TestSummarize(t *testing.T) {
	t.Parallel()
	pr := basePRLong("MERGED",
		withReplies(thread(100, "c1", "fp1", "caller\x00foo", true), "Addressed in c2c2c2c2: done"),
		withReplies(thread(300, "c1", "fp2", "caller\x00bar", true), "Addressed in c3c3c3c3: elsewhere"),
	)
	pr.IssueComments = []string{evidenceBody}
	res, err := Derive(pr, nil, fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(100, 1)}}, t0)
	require.NoError(t, err)
	RecordEvidence(&res.PR, pr.IssueComments, testMarker)
	out := Summarize([]Result{res})
	assert.Contains(t, out, "PRs recorded: 1 (bot-authored 0, with an evidence comment 1)")
	assert.Contains(t, out, "fixed=2")
	assert.Contains(t, out, "reply and geometry agree on 1 of 2")
	assert.Equal(t, "PRs recorded: 0 (bot-authored 0, with an evidence comment 0)\nFindings by disposition: none\n", Summarize(nil))
}
