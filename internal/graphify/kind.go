package graphify

import (
	"path/filepath"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
)

// fileKind is the coarse family a path belongs to. Two nodes are cross-kind
// when their files disagree here, and cross-kind is what the neighbor role is
// looking for: a migration next to the code that writes the row, a Terraform
// block next to the service it stands up.
//
// The extension is the whole rule. A finer classifier would be guessing at
// what a file means from its name, and the graph already knows what is next
// to what — this only has to say whether two files are the same sort of
// thing.
func fileKind(path string) string {
	p := normPath(path)
	ext := strings.ToLower(filepath.Ext(p))
	if ext == "" {
		if base := strings.ToLower(filepath.Base(p)); base != "" {
			return base
		}
		return "other"
	}
	if k, ok := kindByExt[ext]; ok {
		return k
	}
	return strings.TrimPrefix(ext, ".")
}

// kindByExt folds the extensions that are the same kind of artifact. Anything
// absent keeps its own extension as its kind, which errs towards calling two
// files cross-kind: the cost of that is an expansion under a role that ranks
// last, and the cost of the reverse is missing the correlation the graph is
// here for.
var kindByExt = map[string]string{
	".c":        "c",
	".cc":       "cpp",
	".cjs":      "js",
	".cpp":      "cpp",
	".cts":      "ts",
	".h":        "c",
	".hpp":      "cpp",
	".htm":      "html",
	".html":     "html",
	".js":       "js",
	".json":     "config",
	".jsx":      "js",
	".markdown": "docs",
	".md":       "docs",
	".mjs":      "js",
	".mts":      "ts",
	".rst":      "docs",
	".sh":       "shell",
	".tf":       "terraform",
	".tfvars":   "terraform",
	".toml":     "config",
	".ts":       "ts",
	".tsx":      "ts",
	".txt":      "docs",
	".yaml":     "config",
	".yml":      "config",
	".zsh":      "shell",
}

// classOf is the manifest's answer to what a changed file is.
//
// A provider owns the language conventions in this decision and Redline owns
// the language-agnostic layers, so a disagreement resolves in Redline's
// favour on generated files and in the provider's on everything else. This
// adapter speaks no language in particular, so it classifies by path: the
// same evidence a reader has.
func classOf(path string) envelope.Class {
	p := normPath(path)
	base := strings.ToLower(filepath.Base(p))
	lower := strings.ToLower(p)
	switch {
	case lockfiles[base]:
		return envelope.ClassLockfile
	case strings.Contains(lower, "/vendor/") || strings.HasPrefix(lower, "vendor/") ||
		strings.Contains(lower, "node_modules/"):
		return envelope.ClassVendored
	case isTest(base, lower):
		return envelope.ClassTest
	case isMigration(lower):
		return envelope.ClassMigration
	case fileKind(p) == "docs" || fileKind(p) == "config":
		return envelope.ClassOther
	default:
		return envelope.ClassSource
	}
}

var lockfiles = map[string]bool{
	"cargo.lock":        true,
	"composer.lock":     true,
	"gemfile.lock":      true,
	"go.sum":            true,
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
	"poetry.lock":       true,
	"uv.lock":           true,
	"yarn.lock":         true,
}

func isTest(base, lower string) bool {
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"):
		return true
	case strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	case strings.HasPrefix(base, "test_"):
		return true
	case strings.Contains(lower, "/testdata/"), strings.HasPrefix(lower, "testdata/"):
		return true
	case strings.Contains(lower, "/tests/"), strings.HasPrefix(lower, "tests/"):
		return true
	}
	return false
}

func isMigration(lower string) bool {
	if !strings.Contains(lower, "migration") && !strings.Contains(lower, "/migrate/") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(lower))
	return ext == ".sql" || ext == ".go" || ext == ".py" || ext == ".rb" || ext == ".ts"
}
