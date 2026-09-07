package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NormalizeMessage replaces every maximal run of ASCII digits with a single
// '#', so stable parts (symbols, paths, phrasing) survive and volatile numbers
// do not. Same scheme as doctor's function of the same name.
func NormalizeMessage(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	inDigits := false
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if c >= '0' && c <= '9' {
			if !inDigits {
				b.WriteByte('#')
				inDigits = true
			}
			continue
		}
		inDigits = false
		b.WriteByte(c)
	}
	return b.String()
}

// Fingerprint identifies a finding independently of the exact line it sits on.
// Doctor's scheme is file + rule + normalized message. Many Redline findings
// have no file, so the pane's anchor identity substitutes in the file
// position; where a file is known it is used, keeping the two schemes aligned.
// This is the join key for human comment state in comments.json, so it must be
// stable across runs.
func Fingerprint(f Finding) string {
	loc := f.File
	if loc == "" {
		loc = f.Anchor.Key()
	}
	return loc + "\x00" + f.Rule + "\x00" + NormalizeMessage(f.Message)
}

// ShortID is a printable handle for a fingerprint.
//
// A fingerprint is built from NUL-separated parts, which makes it exact and
// makes it unusable as text: it cannot go in a prompt, a model cannot echo it
// back, and it reads as a binary blob anywhere a human looks. The short id is
// a digest of it, so it is stable across runs for the same reason the
// fingerprint is, and it is safe to print, copy, and type.
//
// Ten hex characters is forty bits. Collisions within one report are not a
// correctness problem here: a reference that matched two findings would be
// rendered against both, and the report shows the finding either way.
func ShortID(fingerprint string) string {
	if fingerprint == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fingerprint))
	return "f" + hex.EncodeToString(sum[:])[:10]
}
