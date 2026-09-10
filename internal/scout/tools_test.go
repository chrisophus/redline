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
