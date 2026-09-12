package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool writes an executable named name into dir that prints each of its
// arguments on its own line, so a test can see the exact argv a tool built.
func fakeTool(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// argvAfterSeparator returns the arguments a tool passed as positionals, the
// ones a flag parser reads only because they follow "--".
func argvAfterSeparator(out string) []string {
	args := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// A symbol the model chose can begin with a dash, and gorefactor's flag
// parser would read it as an unknown flag. The "--" separator is what makes
// it a positional argument instead.
func TestGorefactorPassesDashSymbolAsPositional(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "gorefactor")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ts := newToolset(t.TempDir(), "", Limits{})
	out, err := ts.gorefactorContext().run([]byte(`{"symbol":"-Foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := argvAfterSeparator(out); len(got) != 1 || got[0] != "-Foo" {
		t.Errorf("dash symbol was not passed as a positional after --; positionals: %q", got)
	}
}

func TestGraphAffectedPassesDashLabelAsPositional(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "graphify")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ts := newToolset(t.TempDir(), "", Limits{})
	out, err := ts.graphAffected().run([]byte(`{"label":"-Insert"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := argvAfterSeparator(out); len(got) != 1 || got[0] != "-Insert" {
		t.Errorf("dash label was not passed as a positional after --; positionals: %q", got)
	}
}

func TestGraphPathPassesDashEndpointsAsPositionals(t *testing.T) {
	bin := t.TempDir()
	fakeTool(t, bin, "graphify")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ts := newToolset(t.TempDir(), "", Limits{})
	out, err := ts.graphPath().run([]byte(`{"from":"-A","to":"-B"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := argvAfterSeparator(out); len(got) != 2 || got[0] != "-A" || got[1] != "-B" {
		t.Errorf("dash endpoints were not passed as positionals after --; positionals: %q", got)
	}
}

// record() reminds a scout that an untagged record is tied to no question
// and asks it to record the range again with the finding's id. That second
// call replaces the first, so it does not grow the set — but the cap was
// checked before the replacement ran, so at the limit the tool refused the
// very call it had just asked for.
func TestARetagIsAcceptedAtTheRecordCap(t *testing.T) {
	root := tree(t)
	ts := newToolset(root, "", Limits{MaxRecords: 1})
	ts.answering = true
	rec := ts.byName[recordTool]

	args := `{"role":"type","file":"internal/store/user.go","start_line":6,"end_line":8,"symbol":"Insert"`
	if _, err := rec.run([]byte(args + `}`)); err != nil {
		t.Fatalf("the first record was refused: %v", err)
	}
	if _, err := rec.run([]byte(args + `,"answers":"c1"}`)); err != nil {
		t.Fatalf("the retag the tool asks for was refused at the cap: %v", err)
	}
	if len(ts.records) != 1 {
		t.Fatalf("records = %d, want 1: the retag must replace, not add", len(ts.records))
	}
	if ts.records[0].Answers != "c1" {
		t.Errorf("the surviving record is untagged (%q); the retag did not replace it", ts.records[0].Answers)
	}

	// A retag with nowhere to replace still grows the set, so the cap holds.
	other := `{"role":"type","file":"internal/store/user.go","start_line":3,"end_line":3,"answers":"c2"}`
	if _, err := rec.run([]byte(other)); err == nil {
		t.Error("a net-new record at the cap must still be refused")
	}
}

// A question the tree cannot answer has to come back saying so. The scout's
// grep walks this repository and nothing else, so a question about a
// dependency's own code hits an empty search; reported as a bare "no matches"
// the ruling reads it as "searched and found nothing", which is a different
// answer and the wrong one.
func TestAnEmptySearchSaysWhatItSearched(t *testing.T) {
	ts := newToolset(tree(t), "", Limits{})
	out, failed := ts.dispatch("grep", []byte(`{"pattern":"MessageBatchResultUnion"}`))
	if failed {
		t.Fatalf("a search that matched nothing is not a failure: %s", out)
	}
	for _, want := range []string{"this repository", "dependency"} {
		if !strings.Contains(out, want) {
			t.Errorf("an empty search does not say what it covered (%q missing): %q", want, out)
		}
	}
}

// And the scout is told the same thing where it decides what to file: an
// unanswerable question is a note naming why, not a silence.
func TestTheAnsweringBriefSaysWhatIsOutsideTheTree(t *testing.T) {
	got := answerPrompt([]string{"grep"}, 8)
	for _, want := range []string{"not in this repository", "outside the tree"} {
		if !strings.Contains(got, want) {
			t.Errorf("the brief never tells the scout to say %q:\n%s", want, got)
		}
	}
	// A caller question is usually settled by what the callee does with the
	// argument, not by the line that passes it.
	if !strings.Contains(got, "the answer is in the callee") {
		t.Errorf("the brief leaves a caller question answerable by the dispatch site:\n%s", got)
	}
}
