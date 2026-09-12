package scout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The envelope says what the scout settled on. What it tried is the half a
// reader needs when a question came back unanswered, and it used to exist
// only on a terminal nobody had --debug on for.
func TestTheLogKeepsEveryCallInTheTurnItWasMadeIn(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nfunc A() {}\n")
	ts := newToolset(root, "", Limits{})

	ts.startTurn()
	if _, failed := ts.dispatch("grep", []byte(`{"pattern":"func A"}`)); failed {
		t.Fatal("grep of a file that exists failed")
	}
	ts.startTurn()
	if _, failed := ts.dispatch("record", []byte(`{"role":"caller","file":"a.go","start_line":3,"end_line":3,"symbol":"A","found_via":"grep","answers":"c1"}`)); failed {
		t.Fatal("a well-formed record was refused")
	}

	log := logOf(ts, nil)
	if len(log.Calls) != 2 {
		t.Fatalf("calls = %+v, want the grep and the record", log.Calls)
	}
	if log.Calls[0].Tool != "grep" || log.Calls[0].Turn != 1 {
		t.Errorf("first call = %+v, want grep on turn 1", log.Calls[0])
	}
	if !strings.Contains(log.Calls[0].Args, "func A") {
		t.Errorf("the call does not say what it searched for: %q", log.Calls[0].Args)
	}
	if log.Calls[1].Turn != 2 {
		t.Errorf("the second turn's call is logged under turn %d", log.Calls[1].Turn)
	}
	if len(log.Filed) != 1 || log.Filed[0].Answers != "c1" {
		t.Fatalf("filed = %+v, want the record with the question it answers", log.Filed)
	}
	if log.Filed[0].File != "a.go" || log.Filed[0].FoundVia != "grep" {
		t.Errorf("filed = %+v, want the path and how it was found", log.Filed[0])
	}
}

// A refusal is the most useful line in the log: it is a correction the scout
// was given, and a run that spent its last turn being corrected filed nothing
// for a reason nobody could see afterwards.
func TestARefusedCallIsLoggedWithWhyItWasRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a_test.go", "package a\n")
	ts := newToolset(root, "", Limits{})
	ts.startTurn()

	if _, failed := ts.dispatch("record", []byte(`{"role":"caller","file":"a_test.go","start_line":1,"end_line":1,"symbol":"A"}`)); !failed {
		t.Fatal("a test file was recorded; the resolver is meant to refuse it")
	}
	log := logOf(ts, nil)
	if len(log.Calls) != 1 || !log.Calls[0].Failed {
		t.Fatalf("calls = %+v, want one refused call", log.Calls)
	}
	if !strings.Contains(log.Calls[0].Result, "test file") {
		t.Errorf("the log does not carry the correction the scout was given: %q", log.Calls[0].Result)
	}
	if len(log.Filed) != 0 {
		t.Errorf("a refused record was logged as filed: %+v", log.Filed)
	}
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
