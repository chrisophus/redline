package dispositions

import (
	"encoding/hex"
	"testing"
)

func fpMarker(head, fingerprint string) string {
	return "<!-- redline:fp:" + head + ":" + hex.EncodeToString([]byte(fingerprint)) + " -->"
}

func qMarker(key string) string {
	return "<!-- redline:q:" + hex.EncodeToString([]byte(key)) + " -->"
}

func TestParseMarkers_DecodesFingerprintQuestionAndHead(t *testing.T) {
	t.Parallel()
	body := "**Warning · agent** — text\n" + fpMarker("ABCDEF1", "a.go\x00agent-comment\x00msg") + qMarker("caller\x00thing")
	want := Markers{HeadSHA: "abcdef1", Fingerprint: "a.go\x00agent-comment\x00msg", Question: "caller\x00thing"}
	if got := ParseMarkers(body); got != want {
		t.Errorf("ParseMarkers = %+q, want %+q", got, want)
	}
}

func TestParseMarkers_ReviewBodyAndGarbage(t *testing.T) {
	t.Parallel()
	if got := ParseMarkers("<!-- redline:review:DEADBEEF -->").ReviewHead; got != "deadbeef" {
		t.Errorf("ReviewHead = %q, want deadbeef", got)
	}
	// Non-hex payloads and bodies without markers decode to nothing instead of failing.
	for _, body := range []string{
		"<!-- redline:fp:abc:zz -->",
		"plain comment",
		"<!-- redline:fp:abc:abc -->", // odd-length hex
	} {
		if got := ParseMarkers(body); got != (Markers{}) {
			t.Errorf("ParseMarkers(%q) = %+q, want nothing", body, got)
		}
	}
}

const testMarker = "<!-- mct-pr-evidence -->"

const evidenceBody = `<!-- mct-pr-evidence -->
## Evidence

| Claim | Check | Result |
|---|---|---|
| Runs replay | replayed 25 | Verified, 2026-11-05 |
| Old workers tolerate it | recorded then replayed | Verified, 2026-11-05 |
| Nothing calls it | grep | Refuted: one caller |

**Not verified**
- No open executions in prod: needs a login.
- Dev keeps few days of runs.

**Residual risk**
- Very old runs.
`

func TestParseEvidence_CountsClaimsAndGaps(t *testing.T) {
	t.Parallel()
	// Bullets under Not verified count; residual risk bullets do not.
	want := Evidence{Present: true, Verified: 2, Refuted: 1, NotVerified: 2}
	if got := ParseEvidence(evidenceBody, testMarker); got != want {
		t.Errorf("ParseEvidence = %+v, want %+v", got, want)
	}
}

func TestParseEvidence_AbsentWithoutMarker(t *testing.T) {
	t.Parallel()
	if got := ParseEvidence("| a | b | Verified |", testMarker); got != (Evidence{}) {
		t.Errorf("a body without the marker = %+v, want nothing", got)
	}
	if got := ParseEvidence(evidenceBody, ""); got != (Evidence{}) {
		t.Errorf("no marker configured = %+v, want nothing read", got)
	}
}

func TestParseEvidence_NotVerifiedTableRow(t *testing.T) {
	t.Parallel()
	want := Evidence{Present: true, NotVerified: 1}
	if got := ParseEvidence(testMarker+"\n| c | k | Not verified, needs login |\n", testMarker); got != want {
		t.Errorf("ParseEvidence = %+v, want %+v", got, want)
	}
}
