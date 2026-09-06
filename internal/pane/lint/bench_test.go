package lint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/pane"
)

// genIssues builds n realistic lint issues: paths that vary, messages that
// embed digits (byte counts, offsets) and quoted identifiers, exactly the
// shapes fingerprint's digit-normalization exists to collapse.
func genIssues(n int) []Issue {
	tools := []string{"golangci-lint", "eslint"}
	rules := []string{"errcheck", "govet", "unused", "gosec", "staticcheck", "no-console", "no-eval", "no-unused-vars"}
	issues := make([]Issue, n)
	for i := range n {
		issues[i] = Issue{
			Tool:     tools[i%len(tools)],
			File:     fmt.Sprintf("internal/service%d/handler%d.go", i%23, i%97),
			Line:     i%600 + 1,
			Rule:     rules[i%len(rules)],
			Message:  fmt.Sprintf(`unchecked error return value from call to "pkg.Do%d" (%d bytes at offset %d)`, i%31, i*3%4096, i*7%9999),
			Severity: "warning",
		}
	}
	return issues
}

// genDeltaIssues builds a base/head pair shaped like a real run: most issues
// keep identity across a move (line changes, a digit in the message
// changes), a slice resolve (dropped from head), and a slice are newly
// introduced (a different rule head-only).
func genDeltaIssues(n int) (base, head []Issue) {
	base = genIssues(n)
	head = make([]Issue, 0, n)
	for i, iss := range base {
		switch {
		case i%50 == 0:
			continue // resolved: present at base only
		case i%37 == 0:
			introduced := iss
			introduced.Rule = "newrule"
			introduced.Message = fmt.Sprintf(`new violation near "pkg.Fresh%d" at offset %d`, i, i*13%9999)
			head = append(head, introduced)
		default:
			moved := iss
			moved.Line = iss.Line + 250
			moved.Message = fmt.Sprintf(`unchecked error return value from call to "pkg.Do%d" (%d bytes at offset %d)`, i%31, i*3%4096, i*7%9999+1)
			head = append(head, moved)
		}
	}
	return base, head
}

// BenchmarkFingerprint measures the finding-identity normalization: digit
// collapsing over a realistic message shape, the pane's core per-issue cost.
func BenchmarkFingerprint(b *testing.B) {
	issues := genIssues(2000)
	b.ReportAllocs()
	for b.Loop() {
		for _, iss := range issues {
			_ = fingerprint(iss)
		}
	}
}

// BenchmarkDiffIssues measures the base-vs-head multiset match over ~2000
// issues per side: the pane's actual delta computation.
func BenchmarkDiffIssues(b *testing.B) {
	base, head := genDeltaIssues(2000)
	b.ReportAllocs()
	for b.Loop() {
		diffIssues(base, head)
	}
}

// genSuppressionContent builds n lines of source in the given language, one
// directive every 25 lines, matching real-world density better than a
// directive on every line.
func genSuppressionContent(lang string, n int) string {
	var sb strings.Builder
	for i := range n {
		var line string
		switch lang {
		case "go":
			if i%25 == 0 {
				line = fmt.Sprintf("func f%d() error { return doThing(%d) } //nolint:errcheck,gosec", i, i)
			} else {
				line = fmt.Sprintf(`var v%d = "value %d for path /pkg/service%d/handler.go"`, i, i, i%40)
			}
		case "ts":
			if i%25 == 0 {
				line = fmt.Sprintf("console.log(%d) // eslint-disable-line no-console", i)
			} else {
				line = fmt.Sprintf(`const x%d = "value %d at /web/component%d.ts";`, i, i, i%40)
			}
		case "py":
			if i%25 == 0 {
				line = fmt.Sprintf(`x%d = eval(input())  # noqa: E501`, i)
			} else {
				line = fmt.Sprintf(`y%d = "value %d from /scripts/job%d.py"`, i, i, i%40)
			}
		case "rs":
			if i%25 == 0 {
				line = fmt.Sprintf("#[allow(dead_code)] fn unused%d() {}", i)
			} else {
				line = fmt.Sprintf(`let z%d = %d; // ordinary line at /core/module%d.rs`, i, i, i%40)
			}
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// benchRepo is bench_test.go's own throwaway-git-repo helper: lint_test.go's
// repo type is tied to *testing.T and cannot be reused from a *testing.B.
type benchRepo struct {
	b   *testing.B
	dir string
}

func newBenchRepo(b *testing.B) *benchRepo {
	b.Helper()
	r := &benchRepo{b: b, dir: b.TempDir()}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "bench@example.com")
	r.git("config", "user.name", "bench")
	return r
}

func (r *benchRepo) git(args ...string) string {
	r.b.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.b.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *benchRepo) write(path, content string) {
	r.b.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.b.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.b.Fatal(err)
	}
}

func (r *benchRepo) commit(msg string) string {
	r.b.Helper()
	r.git("add", "-A")
	r.git("commit", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *benchRepo) open() *gitx.Repo {
	r.b.Helper()
	g, err := gitx.Open(r.dir)
	if err != nil {
		r.b.Fatal(err)
	}
	return g
}

// BenchmarkSuppressionsScan measures the suppression pane's Diff over ~5000
// added lines spread across four languages, the pane's real hot path
// (per-line regex table scan against the diff's added lines).
func BenchmarkSuppressionsScan(b *testing.B) {
	r := newBenchRepo(b)
	paths := []string{"pkg/big.go", "web/big.ts", "scripts/big.py", "core/big.rs"}
	for _, p := range paths {
		r.write(p, "")
	}
	base := r.commit("base")

	const linesPerFile = 1250
	r.write("pkg/big.go", genSuppressionContent("go", linesPerFile))
	r.write("web/big.ts", genSuppressionContent("ts", linesPerFile))
	r.write("scripts/big.py", genSuppressionContent("py", linesPerFile))
	r.write("core/big.rs", genSuppressionContent("rs", linesPerFile))

	p := &Suppressions{Repo: r.open()}
	p.Scope(paths)

	before, err := p.Observe(pane.Revision{Name: "base", Rev: base})
	if err != nil {
		b.Fatal(err)
	}
	after, err := p.Observe(pane.Worktree)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := p.Diff(before, after); err != nil {
			b.Fatal(err)
		}
	}
}
