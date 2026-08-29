package report

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ccason/redline/internal/packet"
)

// maxShotBytes caps one ingested screenshot and maxShotTotalBytes caps them
// together. report.html is self-contained and embeds images as base64 data
// URIs, which inflates every byte by a third: a diff-scoped walk of half a
// dozen journeys with before/after pairs would otherwise produce a report no
// browser wants to open. The copies under evidence/ui are kept either way,
// so nothing the agent captured is lost — only the inlining stops.
const (
	maxShotBytes      = 3 << 20
	maxShotTotalBytes = 12 << 20
)

// MaterializeShots copies agent screenshots under outDir/evidence/ui and
// returns them as data URIs for the self-contained HTML report.
func MaterializeShots(outDir string, shots []packet.Shot) ([]Screenshot, error) {
	if len(shots) == 0 {
		return nil, nil
	}
	dest := filepath.Join(outDir, "evidence", "ui")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	var out []Screenshot
	total := 0
	for i, s := range shots {
		if strings.TrimSpace(s.Path) == "" {
			return nil, fmt.Errorf("screenshot %d (%s): missing path", i, s.Route)
		}
		afterFile, err := copyShot(dest, i, "after", s.Path, s.Route)
		if err != nil {
			return nil, fmt.Errorf("screenshot %d (%s): %w", i, s.Route, err)
		}
		shot := Screenshot{Route: s.Route, Caption: s.Caption, File: relShot(afterFile, outDir)}
		var beforeFile string
		if strings.TrimSpace(s.Before) != "" {
			beforeFile, err = copyShot(dest, i, "before", s.Before, s.Route)
			if err != nil {
				return nil, fmt.Errorf("screenshot %d (%s) before: %w", i, s.Route, err)
			}
			shot.BeforeFile = relShot(beforeFile, outDir)
		}
		// Inline only while there is budget. Past it the report links to the
		// copies on disk, which the loopback server already serves.
		size := fileSize(afterFile) + fileSize(beforeFile)
		if total+size <= maxShotTotalBytes {
			afterURI, err := dataURI(afterFile)
			if err != nil {
				return nil, err
			}
			shot.After = template.URL(afterURI)
			if beforeFile != "" {
				beforeURI, err := dataURI(beforeFile)
				if err != nil {
					return nil, err
				}
				shot.Before = template.URL(beforeURI)
			}
			total += size
		} else {
			shot.After = template.URL(shot.File)
			if shot.BeforeFile != "" {
				shot.Before = template.URL(shot.BeforeFile)
			}
		}
		out = append(out, shot)
	}
	return out, nil
}

// PruneMissingShots drops captures whose source file is gone. It is applied
// only to screenshots carried forward from an earlier ingest — a walk done in
// a temp directory that has since been cleaned — never to the ones an agent
// just supplied, where a missing file is an error worth hearing about.
func PruneMissingShots(shots []packet.Shot) []packet.Shot {
	out := shots[:0:0]
	for _, s := range shots {
		if _, err := os.Stat(s.Path); err != nil {
			fmt.Fprintf(os.Stderr, "redline: dropping the earlier capture of %s; %s is gone\n", s.Route, s.Path)
			continue
		}
		if s.Before != "" {
			if _, err := os.Stat(s.Before); err != nil {
				s.Before = ""
			}
		}
		out = append(out, s)
	}
	return out
}

func fileSize(path string) int {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(st.Size())
}

// relShot is the path the served report can fetch an image at, relative to
// the evidence directory root.
func relShot(path, outDir string) string {
	if path == "" {
		return ""
	}
	rel, err := filepath.Rel(outDir, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func copyShot(dest string, i int, side, src, route string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return "", err
	}
	if st.Size() > maxShotBytes {
		return "", fmt.Errorf("%s is %d bytes; max is %d", src, st.Size(), maxShotBytes)
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".png"
	}
	label := route
	if label == "" {
		label = filepath.Base(src)
	}
	name := fmt.Sprintf("%02d-%s-%s%s", i+1, slug(label), side, ext)
	dst := filepath.Join(dest, name)
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	return dst, nil
}

func dataURI(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := "image/png"
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	case ".gif":
		mime = "image/gif"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b), nil
}

func slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "shot"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
