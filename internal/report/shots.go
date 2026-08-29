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

// maxShotBytes caps an ingested screenshot. report.html is self-contained and
// embeds images as data URIs; a multi-megabyte PNG would make it unusable.
const maxShotBytes = 3 << 20

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
	for i, s := range shots {
		if strings.TrimSpace(s.Path) == "" {
			return nil, fmt.Errorf("screenshot %d (%s): missing path", i, s.Route)
		}
		afterFile, err := copyShot(dest, i, "after", s.Path, s.Route)
		if err != nil {
			return nil, fmt.Errorf("screenshot %d (%s): %w", i, s.Route, err)
		}
		afterURI, err := dataURI(afterFile)
		if err != nil {
			return nil, err
		}
		shot := Screenshot{Route: s.Route, Caption: s.Caption, After: template.URL(afterURI)}
		if strings.TrimSpace(s.Before) != "" {
			beforeFile, err := copyShot(dest, i, "before", s.Before, s.Route)
			if err != nil {
				return nil, fmt.Errorf("screenshot %d (%s) before: %w", i, s.Route, err)
			}
			beforeURI, err := dataURI(beforeFile)
			if err != nil {
				return nil, err
			}
			shot.Before = template.URL(beforeURI)
		}
		out = append(out, shot)
	}
	return out, nil
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
