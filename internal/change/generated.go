package change

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Generated splits a change into the files a reviewer should see and the files
// that are machine output.
//
// A reviewer does not read generated code. They read the declaration it was
// generated from — the OpenAPI spec, the SQL, the proto — and trust the
// generator. Leaving ogen's output in the change buries the four hand-written
// lines that caused it, and spends the reviewing agent's context on text no
// human will ever act on.
//
// Detection is deliberately conservative, because the failure is asymmetric.
// Hiding a hand-written file is worse than showing a generated one: the
// reviewer never learns it existed. So this looks for markers that generators
// write about themselves and for filenames that only generators produce, and
// everything it excludes is named on the report.
func Generated(dir string, paths []string, attrSet map[string]bool) (kept, generated []string) {
	for _, p := range paths {
		if GeneratedReason(dir, p, attrSet) != "" {
			generated = append(generated, p)
			continue
		}
		kept = append(kept, p)
	}
	return kept, generated
}

// GeneratedReason names why a path is machine output, or returns empty when it
// is not. The reason is reported, so a wrong exclusion is visible rather than
// silent.
func GeneratedReason(dir, path string, attrSet map[string]bool) string {
	if attrSet[path] {
		return "marked linguist-generated"
	}
	if why := generatedByName(path); why != "" {
		return why
	}
	if generatedByHeader(dir, path) {
		return "carries a generated-file marker"
	}
	return ""
}

// generatedByName matches only filenames no human writes by hand. Suffixes
// that a person might plausibly choose — models.go, querier.go, api.ts — are
// left to the header check, which is authoritative when it matches.
func generatedByName(path string) string {
	lower := strings.ToLower(path)
	base := filepath.Base(lower)

	if base == "go.sum" {
		return "checksum file"
	}
	for _, profile := range coverageProfiles {
		if base == profile {
			return "coverage profile"
		}
	}
	for _, lock := range lockfiles {
		if base == lock {
			return "lockfile"
		}
	}
	if lower == "vendor" || strings.HasPrefix(lower, "vendor/") || strings.Contains(lower, "/vendor/") {
		return "vendored dependency"
	}
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return "generated output"
		}
	}
	// kubebuilder and friends prefix rather than suffix.
	if strings.HasPrefix(base, "zz_generated") {
		return "generated output"
	}
	// ogen writes every file in its output package with this prefix.
	if strings.HasPrefix(base, "oas_") && strings.HasSuffix(base, ".go") {
		return "generated output"
	}
	return ""
}

// coverageProfiles are test-runner output. Redline reads one to compute the
// diff coverage number and would otherwise put it in the review as a changed
// file, which is a strange thing to ask a reviewer to read.
var coverageProfiles = []string{
	"coverage.out", "cover.out", "coverage.txt", "c.out",
	"coverage.xml", "lcov.info", "coverage-final.json",
}

var lockfiles = []string{
	"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb",
	"cargo.lock", "poetry.lock", "uv.lock", "composer.lock", "gemfile.lock",
}

var generatedSuffixes = []string{
	".pb.go", ".pb.gw.go", "_grpc.pb.go", ".pb.cc", ".pb.h",
	"_pb2.py", "_pb2_grpc.py",
	".sql.go",
	".min.js", ".min.css", ".js.map", ".css.map",
}

// maxHeaderBytes and maxHeaderLines bound how far into a file the marker is
// looked for. Generators write it at the top, so a match deeper in is far more
// likely to be a file that merely mentions the convention than one that follows
// it.
const (
	maxHeaderBytes = 4096
	maxHeaderLines = 30
)

// generatedMarker matches Go's "Code generated ... DO NOT EDIT." convention,
// anchored at both ends.
//
// Anchoring is the whole point. A substring test for "Code generated" and
// "DO NOT EDIT" also matches any file that discusses the convention — including
// this one, which excluded itself and its own test from a review before the
// anchors went in. That is the asymmetric failure this package is built to
// avoid: a hand-written file silently dropped, and the reviewer never learns it
// existed.
var generatedMarker = regexp.MustCompile(`^\s*(?:(?://+|#+|/\*+|\*|--|<!--)\s*)?Code generated\b.*\bDO NOT EDIT\.?\s*(?:\*/|-->)?\s*$`)

// generatedTag matches the "@generated" tag linguist honours, again anchored:
// the marker has to be what the line says, not something the line talks about.
var generatedTag = regexp.MustCompile(`^\s*(?:(?://+|#+|/\*+|\*|--|<!--)\s*)?@generated\b.*$`)

// generatedByHeader looks for the markers generators write about their own
// output. That is better evidence than any guess about a filename, so long as
// the match is a real marker and not prose.
func generatedByHeader(dir, path string) bool {
	if dir == "" {
		return false
	}
	f, err := os.Open(filepath.Join(dir, path))
	if err != nil {
		// Deleted files, and anything outside the tree, fall back to the name
		// rules. Failing to open is not evidence of anything.
		return false
	}
	defer f.Close()

	read, lines := 0, 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 8192), 65536)
	for sc.Scan() {
		line := sc.Text()
		read += len(line) + 1
		lines++
		if read > maxHeaderBytes || lines > maxHeaderLines {
			return false
		}
		if generatedTag.MatchString(line) || generatedMarker.MatchString(line) {
			return true
		}
	}
	return false
}
