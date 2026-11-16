package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chrisophus/redline/internal/change"
)

// A pull request's session directory holds the latest head at its top level,
// the way it always has. Each head that was reviewed is also copied into a
// directory named by its commit, so the review of an older commit survives the
// next push overwriting review.json and the files beside it:
//
//	.redline/sessions/pr-46/session.json        the latest head
//	.redline/sessions/pr-46/<head-sha>/...      every head that was reviewed
//
// The copy is a session in its own right: --out can point at it and every
// command that reads a session reads it.

var (
	headDirRE = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	hexRE     = regexp.MustCompile(`^[0-9a-f]+$`)
)

// Files that name the revision they were written against. When the one in the
// session directory belongs to another head, it is left out of the copy, so an
// archived head never carries the review of the head before it.
var revisionFiles = []string{"review.json", "postmortem.json"}

// ErrNoHead is what ResolveHead returns when no archived head matches.
var ErrNoHead = errors.New("head not archived")

// IsHeadDir reports whether name is the name of an archived head.
func IsHeadDir(name string) bool { return headDirRE.MatchString(name) }

// ArchiveHead copies the session in dir into dir/<head sha> and returns that
// directory. A session with no head commit, such as a working tree, is not
// archived and returns "". Copying again for the same head replaces the files
// of the first copy, which is how a review that follows a run adds its
// review.json to the archive the run started.
func ArchiveHead(dir string, res *Result) (string, error) {
	if res == nil || res.Change == nil || res.Change.Target == nil {
		return "", nil
	}
	head := strings.ToLower(res.Change.Target.Head)
	if !IsHeadDir(head) {
		return "", nil
	}
	identity := change.ReviewIdentity(res.Report.BaseSHA, res.Change)
	dst := filepath.Join(dir, head)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		src := filepath.Join(dir, e.Name())
		switch {
		case e.IsDir() && e.Name() == "evidence":
			if err := copyTree(src, filepath.Join(dst, e.Name())); err != nil {
				return "", err
			}
		case !e.Type().IsRegular():
			// Head directories, debug captures and anything else that is not a
			// plain file stay where they are.
		case isRevisionFile(e.Name()) && !belongsTo(src, identity):
			// Another head's review. Drop a copy a previous archive made, too.
			_ = os.Remove(filepath.Join(dst, e.Name()))
		default:
			if err := copyFile(src, filepath.Join(dst, e.Name())); err != nil {
				return "", err
			}
		}
	}
	return dst, nil
}

func isRevisionFile(name string) bool {
	for _, f := range revisionFiles {
		if f == name {
			return true
		}
	}
	return false
}

// belongsTo reports whether the file was written against identity. A file with
// no revision stamp, such as a review written by hand, is taken to belong.
func belongsTo(path, identity string) bool {
	buf, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var stamp struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(buf, &stamp) != nil {
		return false
	}
	return stamp.Revision == "" || stamp.Revision == identity
}

func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		switch {
		case e.IsDir():
			if err := copyTree(s, d); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := copyFile(s, d); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Head is one archived head of a session.
type Head struct {
	SHA string
	Dir string
	// Archived is when the head's session was last copied in.
	Archived time.Time
}

// Heads lists the archived heads in dir, newest first.
func Heads(dir string) ([]Head, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var heads []Head
	for _, e := range entries {
		if !e.IsDir() || !IsHeadDir(e.Name()) {
			continue
		}
		h := Head{SHA: e.Name(), Dir: filepath.Join(dir, e.Name())}
		if fi, err := os.Stat(filepath.Join(h.Dir, sessionFile)); err == nil {
			h.Archived = fi.ModTime()
		} else if fi, err := e.Info(); err == nil {
			h.Archived = fi.ModTime()
		}
		heads = append(heads, h)
	}
	sort.Slice(heads, func(i, j int) bool {
		if !heads[i].Archived.Equal(heads[j].Archived) {
			return heads[i].Archived.After(heads[j].Archived)
		}
		return heads[i].SHA < heads[j].SHA
	})
	return heads, nil
}

// ResolveHead finds the archived head of the session in dir that head names,
// which may be a prefix of its commit. It returns the head's directory and
// full commit.
func ResolveHead(dir, head string) (string, string, error) {
	head = strings.ToLower(strings.TrimSpace(head))
	if !hexRE.MatchString(head) {
		return "", "", fmt.Errorf("--head %q: give a commit, or the start of one, in hex", head)
	}
	heads, err := Heads(dir)
	if err != nil {
		return "", "", err
	}
	var match []Head
	for _, h := range heads {
		if strings.HasPrefix(h.SHA, head) {
			match = append(match, h)
		}
	}
	switch len(match) {
	case 1:
		return match[0].Dir, match[0].SHA, nil
	case 0:
		var have []string
		for _, h := range heads {
			have = append(have, h.SHA[:7])
		}
		if len(have) == 0 {
			return "", "", fmt.Errorf("%w: no head %s in %s, and none has been archived there yet", ErrNoHead, head, dir)
		}
		return "", "", fmt.Errorf("%w: no head %s in %s; archived heads are %s", ErrNoHead, head, dir, strings.Join(have, ", "))
	}
	return "", "", fmt.Errorf("--head %s matches %d archived heads in %s; give more of the commit", head, len(match), dir)
}
