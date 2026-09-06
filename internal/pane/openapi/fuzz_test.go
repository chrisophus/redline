package openapi

import "testing"

// FuzzParseSpec feeds parse arbitrary YAML/JSON bytes. parse's contract is
// what Observe and Diff both lean on without rechecking: an error always
// comes back with a nil spec (Observe wraps it into an Unknown, never an
// empty successful diff), and a nil error always comes back with a non-nil
// spec with non-nil operations (Diff indexes straight into it). Either half
// breaking would either mis-report a broken spec as silently clean or panic
// on the very next line.
func FuzzParseSpec(f *testing.F) {
	seeds := []string{
		twoOps,
		"",
		"paths: {}\n",
		"paths: notamap\n",
		"- 1\n- 2\n",
		"paths:\n  /x:\n    get: [\n",
		"paths:\n\t/x:\n\t\tget:\n",
		`{"paths": {"/x": {"get": {"responses": {"200": {"description": "ok"}}}}}}`,
		"openapi: 3.0.0\npaths:\n  /x:\n    get:\n      responses:\n        \"200\":\n          description: ok\n",
		"paths: &a [*a]\n",
		"paths:\n  /x:\n    get:\n      parameters: notalist\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		s, err := parse(raw)
		if err != nil {
			if s != nil {
				t.Fatalf("parse returned both a spec and an error for %q", raw)
			}
			return
		}
		if s == nil {
			t.Fatalf("parse reported no error but returned a nil spec for %q; Diff would treat that as an unbroken contract instead of panicking on it, which is worse", raw)
		}
		for key, op := range s.Ops {
			if op == nil {
				t.Fatalf("parse produced a nil operation for key %q from %q", key, raw)
			}
		}
	})
}
