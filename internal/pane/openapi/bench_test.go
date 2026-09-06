package openapi

import (
	"fmt"
	"strings"
	"testing"
)

// genSpecYAML builds an OpenAPI document with nPaths paths. Even-indexed
// paths get [get, post] (2 operations); odd-indexed paths get
// [get, post, delete] (3 operations), so 80 paths land at exactly 200
// operations, matching a realistic mid-size service contract.
//
// mutate shapes the head side of a comparison: it adds a newly required
// query parameter on some GET operations, makes some POST bodies mandatory,
// adds a newly required schema field on others, and drops a "404" response
// here and there — a realistic mix of breaking and non-breaking edits for
// compare to have real work to do.
func genSpecYAML(nPaths int, mutate bool) string {
	var sb strings.Builder
	sb.WriteString("openapi: 3.0.0\ninfo:\n  title: bench\n  version: 1.0.0\npaths:\n")
	for i := range nPaths {
		fmt.Fprintf(&sb, "  /resource%d/{id}:\n", i)

		sb.WriteString("    get:\n      parameters:\n        - name: id\n          in: path\n          required: true\n")
		if mutate && i%17 == 0 {
			fmt.Fprintf(&sb, "        - name: filter%d\n          in: query\n          required: true\n", i)
		}
		sb.WriteString("      responses:\n        \"200\":\n          description: ok\n")
		if !mutate || i%23 != 0 {
			sb.WriteString("        \"404\":\n          description: missing\n")
		}

		sb.WriteString("    post:\n      requestBody:\n")
		if mutate && i%11 == 0 {
			sb.WriteString("        required: true\n")
		}
		sb.WriteString("        content:\n          application/json:\n            schema:\n              required: [name")
		if mutate && i%13 == 0 {
			sb.WriteString(", email")
		}
		sb.WriteString("]\n")
		sb.WriteString("      responses:\n        \"201\":\n          description: created\n")

		if i%2 == 1 {
			sb.WriteString("    delete:\n      responses:\n        \"204\":\n          description: deleted\n")
		}
	}
	return sb.String()
}

// BenchmarkParseSpec measures parsing an ~80-path, ~200-operation OpenAPI
// document, the pane's per-revision extraction cost.
func BenchmarkParseSpec(b *testing.B) {
	raw := genSpecYAML(80, false)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parse(raw); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompareSpecs measures diffing two realistic ~80-path,
// ~200-operation documents: the pane's actual contract-comparison cost.
func BenchmarkCompareSpecs(b *testing.B) {
	baseRaw := genSpecYAML(80, false)
	headRaw := genSpecYAML(80, true)
	base, err := parse(baseRaw)
	if err != nil {
		b.Fatal(err)
	}
	head, err := parse(headRaw)
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		compare(base, head)
	}
}
