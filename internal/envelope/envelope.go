// Package envelope is the context contract between Redline and a language
// provider. Redline owns this schema and the vocabulary in it; a provider
// fills it in and knows nothing about how it will be spent.
//
// The split is language-specific versus language-agnostic, and it falls
// here: resolving what a change touched needs a language toolchain, and
// deciding how much of the result fits in a model call does not. A provider
// resolves symbols, follows callers, and reads types. It stops at tagging
// each expansion with a role and a priority hint. Redline ranks and truncates
// against those two fields alone, never reading the code inside, which is
// what lets one budgeting implementation serve every language.
//
// Redline links no language toolchain. Providers are separate programs, run
// as subprocesses, and this JSON is the whole interface. See
// docs/context-envelope.md for the wire format a provider writes against.
package envelope

import (
	"fmt"
	"sort"
	"strings"
)

// SchemaVersion guards the JSON encoding of Envelope. A provider states the
// version it wrote; Redline refuses a version it does not know rather than
// reading fields that may have moved.
const SchemaVersion = 1

// Role is the shared vocabulary for what an expansion is. These concepts
// exist in Go, TypeScript, and SQL alike, which is the point: Redline ranks
// and truncates by role without parsing the code the expansion carries.
//
// A provider that has nothing to put in a role omits it. A provider must not
// invent roles: an unknown role ranks last and is reported, because silently
// dropping context reads exactly like a provider that found nothing.
type Role string

const (
	// RoleEnclosing is the full declaration a changed hunk sits inside —
	// the whole function, not the hunk. Always the most valuable expansion:
	// a hunk without its enclosing declaration cannot be judged at all.
	RoleEnclosing Role = "enclosing"
	// RoleCaller is a call site of a changed exported symbol.
	RoleCaller Role = "caller"
	// RoleType is the definition of a type named in a changed signature.
	RoleType Role = "type"
	// RoleSibling is another implementation of an interface the change
	// touches — the other half of "you changed one of these".
	RoleSibling Role = "sibling"
	// RoleTest is a test that covers a changed symbol.
	RoleTest Role = "test"
	// RoleHistory is prior history of the changed lines. It is cheap and it
	// stops a whole class of bad review comment: the suggestion to undo a
	// deliberate fix.
	RoleHistory Role = "history"
)

// roleRank is the order expansions are kept in when the budget binds,
// cheapest and most valuable first. It is Redline's decision, not the
// provider's: a provider that could reorder this could spend Redline's
// budget on whatever it liked.
var roleRank = map[Role]int{
	RoleEnclosing: 0,
	RoleCaller:    1,
	RoleType:      2,
	RoleSibling:   3,
	RoleTest:      4,
	RoleHistory:   5,
}

// unknownRoleRank sorts a role Redline does not know after every role it
// does. Such an expansion is kept if it fits and named in Dropped if not,
// never silently discarded.
const unknownRoleRank = 99

// Rank returns the role's ordering position and whether Redline knows it.
func (r Role) Rank() (int, bool) {
	n, ok := roleRank[r]
	if !ok {
		return unknownRoleRank, false
	}
	return n, true
}

// Roles is every role Redline knows, in rank order. Providers read this from
// docs/context-envelope.md; it is exported for the contract test.
func Roles() []Role {
	out := make([]Role, 0, len(roleRank))
	for r := range roleRank {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i].Rank()
		b, _ := out[j].Rank()
		return a < b
	})
	return out
}

// Class is what a changed file is, for the manifest. Redline decides the
// language-agnostic layers (gitattributes, path patterns) and a provider
// decides the language conventions; the envelope carries the answer so
// neither side needs to know which layer decided.
type Class string

const (
	ClassSource    Class = "source"
	ClassGenerated Class = "generated"
	ClassVendored  Class = "vendored"
	ClassTest      Class = "test"
	ClassMigration Class = "migration"
	ClassLockfile  Class = "lockfile"
	ClassOther     Class = "other"
)

// Provider identifies what produced an envelope. The version is not
// decoration: the language half of the review knowledge lives in the
// provider's repository and is consumed in this one, so a frozen fixture has
// to be able to say which provider wrote it.
type Provider struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Language string `json:"language,omitempty"`
}

// File is one changed file in the manifest, classified.
type File struct {
	Path  string `json:"path"`
	Class Class  `json:"class"`
	// Generated is the answer to "should a reviewer read this", regardless
	// of which side decided. Redline summarizes generated files rather than
	// dropping them, so the count still reaches the model.
	Generated bool `json:"generated,omitempty"`
	// Symbols are the changed symbols in this file, as opaque strings.
	// Redline never parses one; it groups and displays them.
	Symbols []string `json:"symbols,omitempty"`
}

// Expansion is one piece of context beyond the diff, tagged with what it is
// and how much the provider thinks it is worth.
//
// Symbol and Scope are opaque strings. A finding must not be language-shaped,
// and neither must the context that produced it: there is no package field
// here, and anything that only makes sense for one language goes in Details,
// which Redline passes through and renders generically.
type Expansion struct {
	Role Role `json:"role"`
	// Priority is the provider's hint, higher meaning more valuable. It
	// orders expansions within a role and nothing else — a provider cannot
	// promote a test above an enclosing declaration by scoring it 1000.
	Priority int    `json:"priority,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	Scope    string `json:"scope,omitempty"`
	File     string `json:"file,omitempty"`
	// StartLine and EndLine locate Content in File, 1-based and inclusive.
	// Zero means the expansion has no line span (history, for one).
	StartLine int `json:"startLine,omitempty"`
	EndLine   int `json:"endLine,omitempty"`
	// Content is the text itself. Redline measures it and never parses it.
	Content string `json:"content"`
	// Details is provider-defined. Redline renders it generically and makes
	// no decision from it.
	Details map[string]string `json:"details,omitempty"`
}

// Envelope is one provider's answer for one change.
type Envelope struct {
	SchemaVersion int         `json:"schemaVersion"`
	Provider      Provider    `json:"provider"`
	BaseSHA       string      `json:"baseSHA,omitempty"`
	Files         []File      `json:"files,omitempty"`
	Expansions    []Expansion `json:"expansions,omitempty"`

	// PromptFragment is the language half of the review prompt: how this
	// language's code is conventionally reviewed, what its error handling
	// looks like, which of its idioms are load-bearing. Redline supplies the
	// harness half (output schema, silence rules, do not restate priors) and
	// treats this as opaque data it concatenates. Keeping the language half
	// here is what stops Redline acquiring Go knowledge.
	PromptFragment string `json:"promptFragment,omitempty"`

	// Notes are things the provider could not determine. They reach the
	// report as unknowns: a context pack that silently covers half a change
	// reads exactly like one that covers all of it.
	Notes []string `json:"notes,omitempty"`
}

// Validate reports whether an envelope is one Redline can spend. It checks
// the frame, never the contents: a provider that emitted an unreadable
// version, no name, or an expansion with no role at all is a bug worth
// failing loudly for, and a provider that found nothing is a legitimate
// answer. A role Redline does not rank is not a failure here; it is reported
// by UnknownRoles, for the forward compatibility reason stated there.
func (e *Envelope) Validate() error {
	if e == nil {
		return fmt.Errorf("envelope is absent")
	}
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("envelope schema version %d, want %d", e.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(e.Provider.Name) == "" {
		return fmt.Errorf("envelope names no provider")
	}
	for i, x := range e.Expansions {
		if x.Role == "" {
			return fmt.Errorf("expansion %d has no role", i)
		}
	}
	return nil
}

// UnknownRoles lists roles the provider used that Redline does not rank,
// sorted. Reported rather than rejected: the expansions still carry code, and
// a provider one version ahead should degrade rather than fail.
func (e *Envelope) UnknownRoles() []string {
	seen := map[string]bool{}
	for _, x := range e.Expansions {
		if _, ok := x.Role.Rank(); !ok {
			seen[string(x.Role)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// GeneratedFiles returns the paths the manifest marks as machine output,
// sorted. The review summarizes these rather than reading them: one line
// saying how many changed, instead of thirty thousand tokens no reviewer
// would ever act on. Naming them is what keeps a wrong exclusion visible.
func (e *Envelope) GeneratedFiles() []string {
	var out []string
	for _, f := range e.Files {
		if f.Generated {
			out = append(out, f.Path)
		}
	}
	sort.Strings(out)
	return out
}
