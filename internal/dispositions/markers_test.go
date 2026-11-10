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
	m := ParseMarkers(body)
	assert.Equal(t, "abcdef1", m.HeadSHA)
	assert.Equal(t, "a.go\x00agent-comment\x00msg", m.Fingerprint)
	assert.Equal(t, "caller\x00thing", m.Question)
	assert.Empty(t, m.ReviewHead)
}

func TestParseMarkers_ReviewBodyAndGarbage(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "deadbeef", ParseMarkers("<!-- redline:review:DEADBEEF -->").ReviewHead)
	// Non-hex payloads and bodies without markers decode to nothing instead of failing.
	assert.Equal(t, Markers{}, ParseMarkers("<!-- redline:fp:abc:zz -->"))
	assert.Equal(t, Markers{}, ParseMarkers("plain comment"))
	assert.Equal(t, Markers{}, ParseMarkers("<!-- redline:fp:abc:abc -->"), "odd-length hex does not decode")
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
	ev := ParseEvidence(evidenceBody, testMarker)
	assert.True(t, ev.Present)
	assert.Equal(t, 2, ev.Verified)
	assert.Equal(t, 1, ev.Refuted)
	assert.Equal(t, 2, ev.NotVerified, "bullets under Not verified count; residual risk bullets do not")
}

func TestParseEvidence_AbsentWithoutMarker(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Evidence{}, ParseEvidence("| a | b | Verified |", testMarker))
	assert.Equal(t, Evidence{}, ParseEvidence(evidenceBody, ""), "no marker configured, no evidence read")
}

func TestParseEvidence_NotVerifiedTableRow(t *testing.T) {
	t.Parallel()
	ev := ParseEvidence(testMarker+"\n| c | k | Not verified, needs login |\n", testMarker)
	assert.True(t, ev.Present)
	assert.Equal(t, 1, ev.NotVerified)
	assert.Zero(t, ev.Verified)
}
