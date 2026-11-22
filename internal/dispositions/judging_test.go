package dispositions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chrisophus/redline/internal/jev"
)

// fakeJev stands in for TypeSafe. It scores a reply question by what the reply says, and a fix question
// at fixScore, and keeps the last fix state it was shown.
type fakeJev struct {
	fixScore float64
	fixState struct {
		File       string `json:"file"`
		CodeBefore string `json:"code_before"`
		Change     string `json:"change"`
	}
	requests int
}

func (f *fakeJev) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		var req struct {
			State     json.RawMessage            `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		var st struct {
			Reply string `json:"reply"`
		}
		_ = json.Unmarshal(req.State, &st)
		isFix := strings.Contains(string(req.State), `"change"`)
		if isFix {
			_ = json.Unmarshal(req.State, &f.fixState)
		}
		ans := map[string]map[string]float64{}
		for name := range req.Questions {
			ans[name] = map[string]float64{"noul": f.score(name, st.Reply, isFix)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": ans, "usage": map[string]int{"input_tokens": 500}})
	}
}

func (f *fakeJev) score(question, reply string, isFix bool) float64 {
	if isFix {
		return f.fixScore
	}
	kind, _, _ := strings.Cut(question, ".")
	high := map[string]string{
		"open":    "remains open",
		"changed": "fixed",
		"wrong":   "cannot happen",
		"wontfix": "leave it",
	}
	// "fixed because the claim was wrong" is both a fix and an explanation of the bug.
	if kind == "wrong" && strings.Contains(reply, "claim was wrong") {
		return 0.85
	}
	if strings.Contains(reply, high[kind]) {
		return 0.9
	}
	return 0.05
}

type fakeSource struct {
	files map[string][]FileDiff
}

func (f fakeSource) FileAt(commit, path string) (string, error) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String(), nil
}

func (f fakeSource) CommitFiles(commit string) ([]FileDiff, error) {
	if fs, ok := f.files[commit]; ok {
		return fs, nil
	}
	return nil, errors.New("unknown commit")
}

func judging(t *testing.T, f *fakeJev, src Source) *Judging {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := jev.New(srv.URL, "", "k")
	if err != nil {
		t.Fatal(err)
	}
	return &Judging{Ctx: context.Background(), Replies: &jev.Replies{Client: c}, Fixes: &jev.Fixes{Client: c}, Code: src}
}

// replyThread is a Redline thread on a.go line 20, said at c1, with the author's replies.
func replyThread(resolved bool, replies ...string) Thread {
	return withReplies(Thread{
		Path: "a.go", Line: 20, OrigLine: 20, IsResolved: resolved,
		Comments: []ThreadComment{{Body: "**Warning · agent** — a nil map\n" + fpMarker("c1", "fp1"), CreatedAt: t0}},
	}, replies...)
}

func deriveJudged(t *testing.T, pr PR, jd *Judging, d fakeDiff) DispositionRow {
	t.Helper()
	res, err := DeriveJudged(pr, nil, d, t0.Add(48*time.Hour), jd)
	if err != nil {
		t.Fatal(err)
	}
	return res.Dispositions[0]
}

func TestJudged_RemainsOpenReplyCountsAsOpen(t *testing.T) {
	jd := judging(t, &fakeJev{}, fakeSource{})
	// The thread is resolved and a later commit touched the anchor, so geometry alone would say fixed.
	row := deriveJudged(t, basePRLong("OPEN", replyThread(true, "This remains open")), jd,
		fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
	if row.Disposition != string(DispOpen) || row.Basis != BasisJev {
		t.Errorf("disposition %q basis %q, want open from jev", row.Disposition, row.Basis)
	}
	if row.ReplyOpen == nil || *row.ReplyOpen < 0.89 || row.ReplyFixed == nil || *row.ReplyFixed > 0.06 {
		t.Errorf("raw scores not kept: %+v", row)
	}
}

func TestJudged_FixThatExplainsTheBugIsNotADispute(t *testing.T) {
	jd := judging(t, &fakeJev{}, fakeSource{})
	row := deriveJudged(t, basePRLong("MERGED", replyThread(true, "I fixed it, the claim was wrong about the cause but the message was bad")), jd,
		fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
	if row.Disposition != string(DispFixed) || row.Basis != BasisJev || row.DeclineReason != "" {
		t.Errorf("got %q basis %q reason %q, want fixed from jev with no decline reason", row.Disposition, row.Basis, row.DeclineReason)
	}
	if row.FixCommitSha != "c2c2c2c2" {
		t.Errorf("FixCommitSha = %q, want the commit geometry found", row.FixCommitSha)
	}
}

func TestJudged_NotFixingReplyGetsAReason(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"**Not fixing (intentional).** this cannot happen, the map is built above", DeclineWrong},
		{"**Not fixing (intentional).** real, but we leave it", DeclineWontFix},
		{"**Not fixing (intentional).** later", DeclineUnsure},
	} {
		jd := judging(t, &fakeJev{}, fakeSource{})
		row := deriveJudged(t, basePRLong("MERGED", replyThread(true, tc.reply)), jd, fakeDiff{})
		if row.Disposition != string(DispDeclined) || row.Basis != BasisReply || row.DeclineReason != tc.want {
			t.Errorf("%q: got %q basis %q reason %q, want declined by reply with reason %q",
				tc.reply, row.Disposition, row.Basis, row.DeclineReason, tc.want)
		}
	}
}

func TestJudged_UnclearAnswersFallThroughToGeometry(t *testing.T) {
	jd := judging(t, &fakeJev{}, fakeSource{})
	row := deriveJudged(t, basePRLong("MERGED", replyThread(true, "ok")), jd,
		fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
	if row.Disposition != string(DispFixed) || row.Basis != BasisGeometry {
		t.Errorf("got %q basis %q, want fixed by geometry", row.Disposition, row.Basis)
	}
}

func TestJudged_NamedFixIsCheckedAgainstTheCommit(t *testing.T) {
	for _, tc := range []struct {
		score float64
		want  string
	}{{0.9, FixYes}, {0.1, FixNo}, {0.5, FixUnsure}} {
		f := &fakeJev{fixScore: tc.score}
		src := fakeSource{files: map[string][]FileDiff{"c2c2c2c2": {{Path: "a.go", Text: "diff --git a/a.go b/a.go\n+x\n"}}}}
		row := deriveJudged(t, basePRLong("MERGED", replyThread(true, "Addressed in c2c2c2c2: guarded")), judging(t, f, src),
			fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
		if row.FixVerified != tc.want || row.FixScore == nil || *row.FixScore < tc.score-0.001 || *row.FixScore > tc.score+0.001 {
			t.Errorf("score %v: FixVerified %q, FixScore set %v, want %q", tc.score, row.FixVerified, row.FixScore != nil, tc.want)
		}
		if row.Disposition != string(DispFixed) || row.Basis != BasisReply {
			t.Errorf("the marker line still decides: got %q basis %q", row.Disposition, row.Basis)
		}
		// 12 lines either side of line 20.
		if !strings.HasPrefix(f.fixState.CodeBefore, "line 8\n") || !strings.HasSuffix(f.fixState.CodeBefore, "line 32") {
			t.Errorf("code_before = %q", f.fixState.CodeBefore)
		}
	}
}

func TestJudged_AFixInAnotherFileIsShown(t *testing.T) {
	f := &fakeJev{fixScore: 0.9}
	big := func(path string, n int) FileDiff {
		return FileDiff{Path: path, Text: "diff --git a/" + path + " b/" + path + "\n" + strings.Repeat("+x\n", n)}
	}
	src := fakeSource{files: map[string][]FileDiff{"c2c2c2c2": {big("a.go", 2000), big("docs/notes.md", 1500), big("internal/util.go", 100)}}}
	row := deriveJudged(t, basePRLong("MERGED", replyThread(true, "Addressed in c2c2c2c2: the guard moved into util.go")), judging(t, f, src),
		fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
	if row.FixVerified != FixYes {
		t.Fatalf("FixVerified = %q", row.FixVerified)
	}
	got := f.fixState.Change
	a, u, n := strings.Index(got, "diff --git a/a.go"), strings.Index(got, "a/internal/util.go"), strings.Index(got, "a/docs/notes.md")
	if len(got) != changeLimit || a != 0 || u < 0 || (n >= 0 && n < u) {
		t.Errorf("change should hold the anchored file, then util.go, then the rest, cut at %d bytes; got %d bytes, offsets %d %d %d",
			changeLimit, len(got), a, u, n)
	}
}

func TestPickChange(t *testing.T) {
	files := []FileDiff{{"a.go", "AAAA"}, {"b.go", "BBBB"}, {"c.go", "CCCC"}}
	if got := pickChange(files, "c.go", "", 100); got != "AAAABBBBCCCC" {
		t.Errorf("a small diff goes in whole and in order, got %q", got)
	}
	if got := pickChange(files, "c.go", "see b.go", 8); got != "CCCCBBBB" {
		t.Errorf("anchored file, then named file, got %q", got)
	}
	if got := pickChange(files, "c.go", "", 6); got != "CCCCAA" {
		t.Errorf("cut at the limit, got %q", got)
	}
}

func TestJudged_TypeSafeDownMeansOneNoteAndNoAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusInternalServerError) }))
	t.Cleanup(srv.Close)
	c, _ := jev.New(srv.URL, "", "k")
	jd := &Judging{Ctx: context.Background(), Replies: &jev.Replies{Client: c}, Fixes: &jev.Fixes{Client: c}, Code: fakeSource{}}
	pr := basePRLong("MERGED", replyThread(true, "This remains open"), withReplies(Thread{
		Path: "b.go", Line: 5, OrigLine: 5, IsResolved: true,
		Comments: []ThreadComment{{Body: "other\n" + fpMarker("c1", "fp2"), CreatedAt: t0}},
	}, "Addressed in c2c2c2c2"))
	res, err := DeriveJudged(pr, nil, fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}}, t0, jd)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Dispositions {
		if d.ReplyFixed != nil || d.FixVerified != "" || d.Basis == BasisJev {
			t.Errorf("a row carries a Jev answer: %+v", d)
		}
	}
	if n := len(jd.Notes()); n != 1 || !strings.Contains(jd.Notes()[0], "TypeSafe") {
		t.Errorf("notes = %q, want one", jd.Notes())
	}
	if out := Summarize([]Result{res}); strings.Contains(out, "Jev:") {
		t.Errorf("summary has a Jev line with no answers:\n%s", out)
	}
}

func TestSummarize_JevLine(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	res := Result{Dispositions: []DispositionRow{
		{Disposition: "declined", DeclineReason: DeclineWrong, ReplyFixed: p(0.1)},
		{Disposition: "fixed", ReplyFixed: p(0.9), FixVerified: FixYes},
		{Disposition: "fixed", ReplyFixed: p(0.9), FixVerified: FixNo},
		{Disposition: "fixed", ReplyFixed: p(0.9), FixVerified: FixUnsure},
	}}
	want := "Jev: 1 of 4 answered findings disputed as wrong (25%); 1 of 3 named fixes confirmed, 1 doubted\n"
	if out := Summarize([]Result{res}); !strings.Contains(out, want) {
		t.Errorf("summary = %q, want it to contain %q", out, want)
	}
}

func TestSplitDiff(t *testing.T) {
	got := splitDiff("diff --git a/x.go b/x.go\n+1\ndiff --git a/old.go b/new.go\nrename from old.go\n")
	if len(got) != 2 || got[0].Path != "x.go" || got[1].Path != "new.go" || !strings.HasPrefix(got[1].Text, "diff --git a/old.go") {
		t.Errorf("splitDiff = %+v", got)
	}
}

func TestJudged_LaterFreeTextReplyOverrulesAnEarlierMarker(t *testing.T) {
	jd := judging(t, &fakeJev{}, fakeSource{})
	// The marker says fixed, but the newer reply says the problem remains open.
	row := deriveJudged(t, basePRLong("MERGED", replyThread(true, "Addressed in c2c2c2c2: guarded the map", "This remains open")), jd,
		fakeDiff{hunks: map[string][]Hunk{"c2c2c2c2|a.go": edit(20, 1)}})
	if row.Disposition != string(DispOpen) || row.Basis != BasisJev {
		t.Errorf("got %q basis %q, want open from jev", row.Disposition, row.Basis)
	}
}
