package dispositions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/chrisophus/redline/internal/findings"
)

// maxLogLine is the longest reviews.jsonl line read; a model call can log a large prompt.
const (
	logBufStart = 1 << 20
	maxLogLine  = 1 << 24
)

var artifactDirRe = regexp.MustCompile(`^redline-pr-(\d+)-(\d+)$`)

type findingsFile struct {
	Scope    []string `json:"scope"`
	Findings []struct {
		ID          string          `json:"id"`
		Fingerprint string          `json:"fingerprint"`
		File        string          `json:"file"`
		Line        int             `json:"line"`
		StartLine   int             `json:"startLine"`
		Rule        string          `json:"rule"`
		Substrate   string          `json:"substrate"`
		Category    string          `json:"category"`
		Severity    string          `json:"severity"`
		Source      string          `json:"source"`
		Confidence  string          `json:"confidence"`
		Ruling      string          `json:"ruling"`
		Verdict     json.RawMessage `json:"verdict"`
		New         *bool           `json:"new"`
		Question    struct {
			Kind    string `json:"kind"`
			Subject string `json:"subject"`
		} `json:"question"`
	} `json:"findings"`
}

// LoadRuns reads every downloaded Redline artifact directory (redline-pr-<n>-<runid>) under dir. The result
// is grouped by PR number, oldest run first. Directories that do not match, or that hold no findings.json,
// are skipped: a run with nothing to read is not an error. An artifact that cannot be read is skipped too and
// its error is returned in skipped, so one bad download does not hide every other PR's artifacts.
func LoadRuns(dir string) (runs map[int][]Run, skipped []error, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	out := map[int][]Run{}
	for _, e := range entries {
		m := artifactDirRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		pr, _ := strconv.Atoi(m[1])
		runID, _ := strconv.ParseInt(m[2], 10, 64)
		run, ok, err := loadRun(filepath.Join(dir, e.Name()), pr, runID)
		if err != nil {
			skipped = append(skipped, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if ok {
			out[pr] = append(out[pr], run)
		}
	}
	for pr := range out {
		sort.Slice(out[pr], func(i, j int) bool { return out[pr][i].ReviewedAt.Before(out[pr][j].ReviewedAt) })
	}
	return out, skipped, nil
}

func loadRun(dir string, pr int, runID int64) (Run, bool, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "sessions", "*", "findings.json"))
	if len(files) == 0 {
		return Run{}, false, nil
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		return Run{}, false, err
	}
	var ff findingsFile
	if err := json.Unmarshal(raw, &ff); err != nil {
		return Run{}, false, fmt.Errorf("decode findings.json: %w", err)
	}
	at, modelRan := reviewedAt(dir, files[0])
	run := Run{PRNumber: pr, RunID: runID, Scope: ff.Scope, ReviewedAt: at, ModelRan: modelRan}
	for i := range ff.Findings {
		f := &ff.Findings[i]
		if f.New != nil && !*f.New {
			// A finding on code the PR did not change: Redline does not post those by design, so it is not
			// a finding of this PR and must not count as not-posted.
			continue
		}
		run.Findings = append(run.Findings, ArtifactFinding{
			ID: f.ID, Fingerprint: f.Fingerprint, File: f.File, Line: f.Line, StartLine: f.StartLine,
			Rule: f.Rule, Substrate: f.Substrate, Category: f.Category, Severity: f.Severity,
			Source: f.Source, Confidence: f.Confidence, Verdict: verdictOf(f.Ruling, f.Verdict),
			QuestionKey: findings.Question{Kind: findings.QuestionKind(f.Question.Kind), Subject: f.Question.Subject}.Key(),
		})
	}
	return run, true, nil
}

// verdictOf reads the verifying pass's verdict. findings.json writes it as the string field ruling; the
// verdict field holds the agent's later judgment, an object whose ruling is used when the pass left none.
// Older shapes (verdict as a string, or an object with a verdict or status field) are read too. Anything
// else is recorded as empty, not as a decode error.
func verdictOf(ruling string, raw json.RawMessage) string {
	if ruling != "" {
		return ruling
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Ruling  string `json:"ruling"`
		Verdict string `json:"verdict"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, v := range []string{obj.Ruling, obj.Verdict, obj.Status} {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// reviewedAt is the time of the run's last model call in reviews.jsonl, falling back to the findings file's
// modification time when the log is absent (deterministic-only runs write none). modelRan is whether the log
// gave the time: a download need not keep modification times, so the fallback may be when it was downloaded.
func reviewedAt(dir, findingsPath string) (at time.Time, modelRan bool) {
	var last time.Time
	if f, err := os.Open(filepath.Join(dir, "reviews.jsonl")); err == nil {
		defer func() { _ = f.Close() }() // read-only: a close error loses nothing
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, logBufStart), maxLogLine)
		for sc.Scan() {
			var rec struct {
				At time.Time `json:"at"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.At.After(last) {
				last = rec.At
			}
		}
		if sc.Err() != nil {
			last = time.Time{} // a log cut short does not say when the run ended; use the fallback
		}
	}
	if !last.IsZero() {
		return last, true
	}
	if st, err := os.Stat(findingsPath); err == nil {
		last = st.ModTime()
	}
	return last, false
}
