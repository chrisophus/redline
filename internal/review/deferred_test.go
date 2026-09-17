package review

import (
	"context"
	"strings"
	"testing"

	"github.com/chrisophus/redline/internal/change"
	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/findings"
)

// deferredInput is a two-file change with context resolved for one of them:
// the enclosing declaration, a caller in another file, and a type definition
// in a file the change does not touch.
func deferredInput() Input {
	return Input{
		Report: &findings.Report{},
		Change: &change.Set{Files: []change.File{
			{Path: "internal/store/user.go", Diff: "@@ -3,1 +3,2 @@\n type User struct {\n+\tTenantID string\n"},
			{Path: "README.md", Diff: "@@ -1 +1 @@\n-old\n+new\n"},
		}},
		Envelopes: []*envelope.Envelope{{
			Provider: envelope.Provider{Name: "gorefactor", Version: "v1"},
			Expansions: []envelope.Expansion{
				{Role: envelope.RoleEnclosing, Symbol: "User", Scope: "store.User", File: "internal/store/user.go",
					StartLine: 3, EndLine: 8, Content: "type User struct {\n\tID string\n\tTenantID string\n}\n"},
				{Role: envelope.RoleCaller, Symbol: "User", Scope: "store.User", File: "internal/api/handler.go",
					StartLine: 40, EndLine: 44, Content: "u := store.User{ID: id}\nstore.Insert(u)\n",
					Details: map[string]string{"callerSymbol": "api.Create"}},
				{Role: envelope.RoleType, Symbol: "Tenant", Scope: "store.Tenant", File: "internal/tenant/tenant.go",
					StartLine: 1, EndLine: 3, Content: "type Tenant struct{ ID string }\n"},
			},
		}},
	}
}

// With DeferContext the context is not in the prompt. A caller in another
// file is indexed under the changed file its scope leads to, and an entry
// whose scope leads nowhere is listed for the whole change.
func TestDeferredContextIsIndexedBesideTheDiff(t *testing.T) {
	res, err := Assemble(deferredInput(), Options{Model: "claude-sonnet-5", DeferContext: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Prompt, "store.Insert(u)") {
		t.Fatal("the caller's content reached the prompt; it should be held back")
	}
	at := strings.Index(res.Prompt, "### internal/store/user.go")
	if at < 0 {
		t.Fatalf("the changed file's section is missing:\n%s", res.Prompt)
	}
	user := res.Prompt[at:]
	if next := strings.Index(user, "### README.md"); next > 0 {
		user = user[:next]
	}
	if !strings.Contains(user, "caller User at internal/api/handler.go:40-44") {
		t.Errorf("the caller is not indexed under the file its scope leads to:\n%s", user)
	}
	if !strings.Contains(res.Prompt, "## Context for the whole change") ||
		!strings.Contains(res.Prompt, "type Tenant at internal/tenant/tenant.go:1-3") {
		t.Errorf("an entry whose scope leads to no changed file must be listed for the whole change:\n%s", res.Prompt)
	}
	if !strings.Contains(callsBlock(StageFindings, true), "get_context") {
		t.Error("the calls block must say context can be read")
	}
	plain, err := Assemble(deferredInput(), Options{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain.Prompt, "store.Insert(u)") || strings.Contains(plain.Prompt, "get_context") {
		t.Error("without the flag the context goes into the prompt as before and nothing is indexed")
	}
}

// A pass reads held-back context mid-pass: the call is answered with the
// content, records nothing, and the pass goes on to its comments.
func TestAPassReadsHeldBackContext(t *testing.T) {
	api := serveSSE(t,
		reply([2]string{CallContext, `{"ids":["ctx2","ctx99"]}`}),
		reply([2]string{CallComment, goodComment}, [2]string{CallDone, `{}`}),
	)
	res, err := Assemble(deferredInput(), Options{Model: "claude-sonnet-5", DeferContext: true})
	if err != nil {
		t.Fatal(err)
	}
	res = res.judgingRequest(true)
	out, err := runOnce(context.Background(), deferredInput(), loopOpts(api), res)
	if err != nil {
		t.Fatal(err)
	}
	if out.Fetched != 2 || len(out.Review.Comments) != 1 || out.Rejected != 0 {
		t.Fatalf("fetched=%d comments=%d rejected=%d, want 2, 1, 0", out.Fetched, len(out.Review.Comments), out.Rejected)
	}
	second := string(api.seen()[1])
	if !strings.Contains(second, `store.Insert(u)`) || !strings.Contains(second, "callerSymbol: api.Create") {
		t.Error("the answer to get_context must carry the entry's content and details")
	}
	if !strings.Contains(second, "ctx99: no such id") {
		t.Error("an id that names nothing must be said so")
	}
}
