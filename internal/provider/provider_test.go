package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectFindsNothingInAnUnconfiguredRepo(t *testing.T) {
	got, err := Detect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an unconfigured repository has no provider, got %v", got)
	}
}

func TestDetectReadsTheOptInConfigFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gorefactor.yaml", "lint: {}\n")
	got, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Command == "" {
		t.Fatalf("a repository carrying the tool's own config opted in, got %v", got)
	}
}

func TestDeclaredEntryReplacesTheBuiltinOfTheSameName(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gorefactor.yaml", "lint: {}\n")
	write(t, dir, ".redline.yml", `
context:
  - name: gorefactor
    command: /custom/path/gorefactor
    args: ["context", "--changed", "{{base}}"]
    scope: ["**/*.go"]
`)
	got, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("an override must replace, not duplicate: %v", got)
	}
	if got[0].Command != "/custom/path/gorefactor" {
		t.Fatalf("declared entry did not win, got %q", got[0].Command)
	}
}

func TestDetectAddsADeclaredProviderForAnotherLanguage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".redline.yml", `
context:
  - name: tsctx
    command: tsctx
    args: ["--base", "{{base}}"]
    scope: ["**/*.ts"]
`)
	got, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "tsctx" {
		t.Fatalf("a second language needs a config entry and no code change, got %v", got)
	}
}

func TestBrokenConfigStillReportsTheDetectedBuiltin(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".gorefactor.yaml", "lint: {}\n")
	write(t, dir, ".redline.yml", "context: [oh no\n")
	got, err := Detect(dir)
	if err == nil {
		t.Fatal("a misconfigured .redline.yml must fail the layer visibly")
	}
	if len(got) != 1 {
		t.Fatal("a broken config must not silently disable a provider the repository plainly has")
	}
}

func TestClaimedIsTheProvidersScope(t *testing.T) {
	c := Config{Scope: []string{"**/*.go"}}
	got := c.Claimed([]string{"a/b.go", "web/x.ts", "c.go"})
	if len(got) != 2 || got[0] != "a/b.go" || got[1] != "c.go" {
		t.Fatalf("Claimed() = %v", got)
	}
}

const bare = `{"schemaVersion":1,"provider":{"name":"p","version":"v1"},
"expansions":[{"role":"enclosing","content":"func F() {}"}]}`

func TestParseAcceptsABareEnvelope(t *testing.T) {
	env, err := Parse([]byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	if len(env.Expansions) != 1 {
		t.Fatalf("expansions = %d, want 1", len(env.Expansions))
	}
}

func TestParseUnwrapsAnOkDataResult(t *testing.T) {
	env, err := Parse([]byte(`{"ok":true,"error":"","data":` + bare + `}`))
	if err != nil {
		t.Fatalf("a provider must not have to break its own CLI contract: %v", err)
	}
	if env.Provider.Name != "p" {
		t.Fatalf("provider = %q", env.Provider.Name)
	}
}

func TestParseSurfacesAProvidersOwnFailure(t *testing.T) {
	_, err := Parse([]byte(`{"ok":false,"error":"packages failed to load","data":{}}`))
	if err == nil {
		t.Fatal("a provider reporting failure must not read as an empty context")
	}
}

func TestParseRejectsEmptyOutput(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Fatal("no output is a failure, not an empty context")
	}
}

func TestParseRejectsAnUnknownSchemaVersion(t *testing.T) {
	_, err := Parse([]byte(`{"schemaVersion":99,"provider":{"name":"p"}}`))
	if err == nil {
		t.Fatal("reading fields that may have moved is worse than failing")
	}
}
