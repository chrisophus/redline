package post

import (
	"fmt"
	"os"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
	"gopkg.in/yaml.v3"
)

// Profile tells post how to stamp a review so a merge gate can read it.
// Without one, post still writes a COMMENT review; it just does not emit
// pass/fail markers or enforce author/HEAD checks.
//
// Markers are repository policy, not Redline's identity. Marketplace's gate
// looks for mct-agent-review:v1; another repo can name its own.
type Profile struct {
	ReviewMarker  string
	FindingMarker string
	Blocking      []findings.Severity
	AuthorOnly    bool
	RequireHead   bool
	// FailClosedPane turns a pane that applied to the change but did not run
	// into fail, even when no findings were produced. A missing check must not
	// read as pass.
	FailClosedPane bool
}

type profileFile struct {
	ReviewMarker   string   `yaml:"review_marker"`
	FindingMarker  string   `yaml:"finding_marker"`
	Blocking       []string `yaml:"blocking"`
	AuthorOnly     *bool    `yaml:"author_only"`
	RequireHead    *bool    `yaml:"require_head"`
	FailClosedPane *bool    `yaml:"fail_closed_pane"`
}

// LoadProfile reads a YAML profile. Missing optional fields default to the
// strict merge-gate shape: error and warning block, author-only, HEAD must
// match, a pane that applied and did not run is fail.
func LoadProfile(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	var f profileFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("profile %s: %w", path, err)
	}
	if strings.TrimSpace(f.ReviewMarker) == "" || strings.TrimSpace(f.FindingMarker) == "" {
		return nil, fmt.Errorf("profile %s: review_marker and finding_marker are required", path)
	}
	p := &Profile{
		ReviewMarker:   strings.TrimSpace(f.ReviewMarker),
		FindingMarker:  strings.TrimSpace(f.FindingMarker),
		AuthorOnly:     true,
		RequireHead:    true,
		FailClosedPane: true,
	}
	if f.AuthorOnly != nil {
		p.AuthorOnly = *f.AuthorOnly
	}
	if f.RequireHead != nil {
		p.RequireHead = *f.RequireHead
	}
	if f.FailClosedPane != nil {
		p.FailClosedPane = *f.FailClosedPane
	}
	if len(f.Blocking) == 0 {
		p.Blocking = []findings.Severity{findings.SeverityError, findings.SeverityWarning}
	} else {
		for _, s := range f.Blocking {
			sev, err := parseSeverity(s)
			if err != nil {
				return nil, fmt.Errorf("profile %s: %w", path, err)
			}
			p.Blocking = append(p.Blocking, sev)
		}
	}
	return p, nil
}

func parseSeverity(s string) (findings.Severity, error) {
	switch findings.Severity(strings.ToLower(strings.TrimSpace(s))) {
	case findings.SeverityError:
		return findings.SeverityError, nil
	case findings.SeverityWarning:
		return findings.SeverityWarning, nil
	case findings.SeverityInfo:
		return findings.SeverityInfo, nil
	default:
		return "", fmt.Errorf("unknown blocking severity %q (want error, warning, or info)", s)
	}
}

// Blocks reports whether this severity fails the gate.
func (p *Profile) Blocks(s findings.Severity) bool {
	if p == nil {
		return false
	}
	for _, b := range p.Blocking {
		if b == s {
			return true
		}
	}
	return false
}

// GateVerdict is pass when nothing blocking is present and every pane that
// applied to the change ran. fail otherwise. Empty profile yields an empty
// string: post is not attesting.
func GateVerdict(rep *findings.Report, p *Profile) string {
	if p == nil {
		return ""
	}
	if p.FailClosedPane && rep != nil && len(rep.FailedSubstrates()) > 0 {
		return "fail"
	}
	if rep != nil {
		for _, f := range rep.Findings {
			if p.Blocks(f.Severity) {
				return "fail"
			}
		}
	}
	return "pass"
}

// reviewAttestMarker is the hidden line a merge gate scans for.
func reviewAttestMarker(p *Profile, verdict, head string) string {
	if p == nil {
		return ""
	}
	return marker(fmt.Sprintf("%s verdict=%s head=%s", p.ReviewMarker, verdict, head))
}

// findingAttestMarker tags one blocking inline finding. Severity is mapped to
// the high/medium/low vocabulary GitHub review gates already parse.
func findingAttestMarker(p *Profile, s findings.Severity) string {
	if p == nil || !p.Blocks(s) {
		return ""
	}
	return marker(fmt.Sprintf("%s severity=%s", p.FindingMarker, gateSeverity(s)))
}

func gateSeverity(s findings.Severity) string {
	switch s {
	case findings.SeverityError:
		return "high"
	case findings.SeverityWarning:
		return "medium"
	default:
		return "low"
	}
}

// AttestedVerdict returns the last attesting review's verdict for this head,
// or empty if none.
func AttestedVerdict(bodies []string, markerName, head string) string {
	if markerName == "" || head == "" {
		return ""
	}
	var last string
	prefix := markerName
	for _, body := range bodies {
		for _, line := range strings.Split(body, "\n") {
			if !strings.Contains(line, prefix) {
				continue
			}
			if parseMarkerField(line, "head") != head {
				continue
			}
			if v := parseMarkerField(line, "verdict"); v != "" {
				last = v
			}
		}
	}
	return last
}

func parseMarkerField(line, field string) string {
	key := field + "="
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	v := line[i+len(key):]
	if j := strings.IndexAny(v, " >"); j >= 0 {
		v = v[:j]
	}
	return strings.TrimSpace(v)
}
