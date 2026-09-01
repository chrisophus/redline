package change_test

import (
	"testing"

	"github.com/ccason/redline/internal/change"
)

func TestAreasClassifyFiles(t *testing.T) {
	cases := map[string]string{
		"migrations/000001_init.up.sql":           "sql",
		"api/openapi.yaml":                        "api",
		"web/src/Button.tsx":                      "ui",
		"web/index.html":                          "ui",
		"internal/report/assets/report.html.tmpl": "ui",
		"internal/run/run.go":                     "code",
		"internal/run/run_test.go":                "tests",
	}
	for path, want := range cases {
		areas := change.Areas(path)
		var found bool
		for _, a := range areas {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected area %q, got %v", path, want, areas)
		}
	}
}
