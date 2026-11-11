package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/dispositions"
)

// cmdDispositions records what became of each review finding on the named
// pull requests: fixed, declined, open, stale, or never posted. It covers
// Redline's findings and the threads other review bots and people opened. It
// reads GitHub and git with the caller's own gh login, never writes to
// GitHub, and writes JSONL files for a loader to copy into a database.
func cmdDispositions(o opts) error {
	return runDispositions(context.Background(), o, dispositions.ExecRunner{}, os.Stdout, os.Stderr)
}

func runDispositions(ctx context.Context, o opts, runner dispositions.Runner, stdout, stderr io.Writer) error {
	gh, err := dispositionsRepo(ctx, runner, o.repo)
	if err != nil {
		return err
	}
	numbers, err := dispositionPRs(ctx, gh, o)
	if err != nil {
		return err
	}
	runs := map[int][]dispositions.Run{}
	if o.artifacts != "" {
		var skipped []error
		runs, skipped, err = dispositions.LoadRuns(o.artifacts)
		if err != nil {
			return err
		}
		for _, e := range skipped {
			_, _ = fmt.Fprintf(stderr, "redline: skipped artifact %v\n", e)
		}
	}
	rec := dispositions.Recorder{GH: gh, Git: runner, EvidenceMarker: o.evidenceMarker}
	var results []dispositions.Result
	failed := 0
	// One PR that cannot be read must not hide the rest.
	for _, n := range numbers {
		res, err := rec.Record(ctx, n, runs[n])
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "redline: PR %d: %v\n", n, err)
			failed++
			continue
		}
		results = append(results, res)
	}
	if _, err := fmt.Fprint(stdout, dispositions.Summarize(results)); err != nil {
		return err
	}
	// When every PR failed there is nothing to write, and writing would empty the files a previous run left.
	if o.jsonl != "" && len(results) > 0 {
		if err := writeDispositions(o.jsonl, results); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d PRs could not be recorded", failed, len(numbers))
	}
	return nil
}

// dispositionsRepo returns a GitHub reader for repo, or for the current
// repository when repo is empty.
func dispositionsRepo(ctx context.Context, runner dispositions.Runner, repo string) (dispositions.GitHub, error) {
	if repo == "" {
		out, err := runner.Run(ctx, "gh", "repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
		if err != nil {
			return dispositions.GitHub{}, err
		}
		repo = strings.TrimSpace(string(out))
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return dispositions.GitHub{}, fmt.Errorf("--repo must be OWNER/NAME, got %q", repo)
	}
	return dispositions.GitHub{Owner: owner, Name: name, R: runner}, nil
}

func dispositionPRs(ctx context.Context, gh dispositions.GitHub, o opts) ([]int, error) {
	if o.pr != "" && o.recent > 0 {
		return nil, errors.New("pass --pr or --recent, not both")
	}
	if o.pr != "" {
		var out []int
		for s := range strings.SplitSeq(o.pr, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid PR number %q", s)
			}
			out = append(out, n)
		}
		return out, nil
	}
	if o.recent > 0 {
		return gh.FetchRecentPRNumbers(ctx, o.recent)
	}
	return nil, errors.New("no pull requests given: pass --pr N[,N...] or --recent N")
}

func writeDispositions(dir string, results []dispositions.Result) error {
	prs := make([]dispositions.PRRow, 0, len(results))
	var finds []dispositions.FindingRow
	var disps []dispositions.DispositionRow
	for i := range results {
		r := &results[i]
		prs = append(prs, r.PR)
		finds = append(finds, r.Findings...)
		disps = append(disps, r.Dispositions...)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSONLFile(filepath.Join(dir, dispositions.FilePRs), prs); err != nil {
		return err
	}
	if err := writeJSONLFile(filepath.Join(dir, dispositions.FileFindings), finds); err != nil {
		return err
	}
	return writeJSONLFile(filepath.Join(dir, dispositions.FileDispositions), disps)
}

func writeJSONLFile[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := dispositions.WriteJSONL(f, rows); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
