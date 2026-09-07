package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRejectsForeignSchemaVersion(t *testing.T) {
	e := &Envelope{SchemaVersion: SchemaVersion + 1, Provider: Provider{Name: "p"}}
	if err := e.Validate(); err == nil {
		t.Fatal("a provider one schema version ahead must be refused, not read")
	}
}

func TestValidateRequiresProviderIdentity(t *testing.T) {
	e := &Envelope{SchemaVersion: SchemaVersion}
	if err := e.Validate(); err == nil {
		t.Fatal("an envelope with no provider cannot be attributed in a fixture")
	}
}

func TestValidateAcceptsEmptyExpansions(t *testing.T) {
	e := &Envelope{SchemaVersion: SchemaVersion, Provider: Provider{Name: "p", Version: "v1"}}
	if err := e.Validate(); err != nil {
		t.Fatalf("a provider that found nothing is a legitimate answer: %v", err)
	}
}

func TestUnknownRoleIsReportedNotRejected(t *testing.T) {
	e := &Envelope{
		SchemaVersion: SchemaVersion,
		Provider:      Provider{Name: "p", Version: "v1"},
		Expansions:    []Expansion{{Role: "invented", Content: "x"}},
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("an unknown role must degrade, not fail: %v", err)
	}
	got := e.UnknownRoles()
	if len(got) != 1 || got[0] != "invented" {
		t.Fatalf("UnknownRoles() = %v, want [invented]", got)
	}
}

func TestKnownRolesRankInCostOrder(t *testing.T) {
	want := []Role{RoleEnclosing, RoleCaller, RoleType, RoleSibling, RoleTest, RoleHistory}
	got := Roles()
	if len(got) != len(want) {
		t.Fatalf("Roles() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Roles()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGeneratedFilesAreNamedNotCounted(t *testing.T) {
	e := &Envelope{Files: []File{
		{Path: "b.pb.go", Generated: true},
		{Path: "a.go"},
		{Path: "a.pb.go", Generated: true},
	}}
	got := e.GeneratedFiles()
	if len(got) != 2 || got[0] != "a.pb.go" || got[1] != "b.pb.go" {
		t.Fatalf("GeneratedFiles() = %v, want sorted [a.pb.go b.pb.go]", got)
	}
}

// The JSON tags are the contract a provider in another repository writes
// against. Renaming one silently breaks every provider, so pin them.
func TestWireFieldNamesArePinned(t *testing.T) {
	buf, err := json.Marshal(Envelope{
		SchemaVersion: 1,
		Provider:      Provider{Name: "p", Version: "v", Language: "go"},
		BaseSHA:       "abc",
		Files:         []File{{Path: "a.go", Class: ClassSource, Generated: true, Symbols: []string{"F"}}},
		Expansions: []Expansion{{
			Role: RoleEnclosing, Priority: 3, Symbol: "F", Scope: "pkg.F",
			File: "a.go", StartLine: 1, EndLine: 2, Content: "x",
			Details: map[string]string{"k": "v"},
		}},
		PromptFragment: "frag",
		Notes:          []string{"n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"schemaVersion"`, `"provider"`, `"name"`, `"version"`, `"language"`,
		`"baseSHA"`, `"files"`, `"path"`, `"class"`, `"generated"`, `"symbols"`,
		`"expansions"`, `"role"`, `"priority"`, `"symbol"`, `"scope"`,
		`"startLine"`, `"endLine"`, `"content"`, `"details"`,
		`"promptFragment"`, `"notes"`,
	} {
		if !strings.Contains(string(buf), key) {
			t.Errorf("wire format lost %s; providers in other repositories write against these names", key)
		}
	}
}
