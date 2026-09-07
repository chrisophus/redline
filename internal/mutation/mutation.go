// Package mutation ingests a gomutants report (github.com/szhekpisov/gomutants)
// and scopes it to the lines a change adds. Coverage says a test ran a line;
// mutation says whether a test would fail if that line were wrong. A mutant that
// lived is a line a test executes but nothing asserts, which is the gap this puts
// on top of the coverage number.
//
// Like the cover package, this reads a report the repository already produced
// rather than running mutation testing itself. A full run is minutes of repeated
// test executions, not something to do on every redline run, so redline reads
// mutants.json when it is there and says nothing when it is not.
package mutation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// reportNames are the conventional places a gomutants JSON report lands. There
// is no standard, so this is a search rather than a lookup, the same as the
// cover package's profile search.
var reportNames = []string{"mutants.json", ".mutants.json", "gomutants.json"}

// Locate finds a gomutants report under root, or returns "". Absence is a normal
// answer: most repositories have no mutation report, and that is not a failure.
func Locate(root string) string {
	for _, n := range reportNames {
		if fi, err := os.Stat(filepath.Join(root, n)); err == nil && !fi.IsDir() {
			return n
		}
	}
	return ""
}

// rawMutant mirrors one entry of a gomutants report's per-file mutations list.
type rawMutant struct {
	Type        string `json:"type"`
	Status      string `json:"status"`
	Line        int    `json:"line"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
}

type rawFile struct {
	FileName  string      `json:"file_name"`
	Mutations []rawMutant `json:"mutations"`
}

type raw struct {
	Module string    `json:"go_module"`
	Files  []rawFile `json:"files"`
}

// Mutant is one surviving mutation on a line the change added: a test runs the
// line, but nothing fails when the line is changed this way. Original and
// Replacement name the change that went uncaught, which is the assertion a test
// is missing.
type Mutant struct {
	Line        int    `json:"line"`
	Mutator     string `json:"mutator"`
	Original    string `json:"original,omitempty"`
	Replacement string `json:"replacement,omitempty"`
}

// FileSurvivors is one file's surviving mutants on the lines a change added.
type FileSurvivors struct {
	Path    string   `json:"path"`
	Mutants []Mutant `json:"mutants"`
}

// Result is the diff-scoped mutation picture: of the mutants that sit on the
// lines this change adds, how many a test killed, how many lived, and the
// survivors themselves. Killed and Lived count only lines this change touched,
// so the number describes the change and not the whole repository.
type Result struct {
	Report   string          `json:"report"`
	Killed   int             `json:"killed"`
	Lived    int             `json:"lived"`
	Survived []FileSurvivors `json:"survived,omitempty"`
}

// Changed is one changed file and the new-side line numbers it added.
type Changed struct {
	Path  string
	Added []int
}

// Compute reads the gomutants report under root and intersects it with the lines
// each changed file adds. Returns nil when no report is found, or when the report
// says nothing about any line this change touched: both are "nobody measured
// this change", which must not render as "the change is fully asserted".
func Compute(root string, changed []Changed) *Result {
	name := Locate(root)
	if name == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return nil
	}

	res := &Result{Report: name}
	for _, c := range changed {
		muts := mutationsFor(r, c.Path)
		if muts == nil {
			continue
		}
		added := make(map[int]bool, len(c.Added))
		for _, l := range c.Added {
			added[l] = true
		}
		var survivors []Mutant
		for _, m := range muts {
			if !added[m.Line] {
				continue
			}
			switch normalize(m.Status) {
			case "killed":
				res.Killed++
			case "lived":
				res.Lived++
				survivors = append(survivors, Mutant{
					Line: m.Line, Mutator: m.Type, Original: m.Original, Replacement: m.Replacement,
				})
			}
			// not-covered, not-viable and timed-out are left out. Coverage already
			// reports lines no test runs, and the other two are mutants gomutants
			// could not decide on, not a statement about the tests.
		}
		if len(survivors) > 0 {
			res.Survived = append(res.Survived, FileSurvivors{Path: c.Path, Mutants: survivors})
		}
	}
	if res.Killed == 0 && res.Lived == 0 {
		return nil
	}
	return res
}

// mutationsFor matches a repository path against gomutants' file_name. That name
// is relative to the common ancestor of the paths gomutants was pointed at: run
// on ./... from the module root it is repository-relative and matches exactly, a
// narrower target makes it a path suffix. Match by longest path-component suffix,
// the same tactic cover.blocksFor uses for import-qualified profile keys.
func mutationsFor(r raw, path string) []rawMutant {
	var best []rawMutant
	bestLen := -1
	for i := range r.Files {
		f := r.Files[i]
		if f.FileName == path || strings.HasSuffix(path, "/"+f.FileName) {
			if len(f.FileName) > bestLen {
				best, bestLen = f.Mutations, len(f.FileName)
			}
		}
	}
	return best
}

// normalize folds a gomutants status ("NOT COVERED") to a lowercase hyphenated
// token ("not-covered").
func normalize(status string) string {
	return strings.ReplaceAll(strings.ToLower(status), " ", "-")
}
