package lint

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/gitx"
	"github.com/ccason/redline/internal/pane"
)

// ConfigSubstrate is the lint-config pane's name in the findings schema.
const ConfigSubstrate = "redline/lint-config"

// Config reports lint-configuration drift: a rule this change disables,
// downgrades, or newly excludes. A lint delta earned by turning a rule off
// looks identical to one earned by fixing code unless something names the
// rule, and this pane is that something.
//
// Enumeration is per format. golangci YAML and eslintrc JSON are parsed and
// their rule changes named; formats Redline cannot parse (a JS eslint config,
// a TOML golangci config) still produce a finding that the config changed,
// with the diff as evidence, so drift is never silent just because it is
// unparseable.
type Config struct {
	Repo *gitx.Repo

	scoped []string
}

// lintConfigNames is every filename either tool reads, plus the ignore file.
var lintConfigNames = map[string]bool{}

func init() {
	for _, n := range golangciConfigs {
		lintConfigNames[n] = true
	}
	for _, n := range eslintConfigs {
		lintConfigNames[n] = true
	}
	lintConfigNames[".eslintignore"] = true
}

func isLintConfig(path string) bool {
	return lintConfigNames[filepath.Base(path)]
}

// Name implements pane.Pane.
func (p *Config) Name() string { return ConfigSubstrate }

// Scope is the changed lint configs.
func (p *Config) Scope(changed []string) []string {
	var out []string
	for _, path := range changed {
		if isLintConfig(path) {
			out = append(out, path)
		}
	}
	p.scoped = out
	return out
}

type configObservation struct{ Rev string }

func (s *configObservation) ID() string {
	rev := s.Rev
	if rev == "" {
		rev = "worktree"
	}
	return "lint-config@" + rev
}

// Observe records the revision; Diff reads both sides of each scoped config.
func (p *Config) Observe(rev pane.Revision) (pane.Observation, error) {
	return &configObservation{Rev: rev.Rev}, nil
}

// Diff names what each changed config turns off.
func (p *Config) Diff(before, after pane.Observation) (pane.Result, error) {
	base, ok := before.(*configObservation)
	if !ok {
		return pane.Result{}, fmt.Errorf("lint-config: base observation is %T", before)
	}
	res := pane.Result{Evidence: map[string]pane.Artifact{}}
	var lines []string

	paths := append([]string(nil), p.scoped...)
	sort.Strings(paths)
	for _, path := range paths {
		baseRaw := p.Repo.File(base.Rev, path)
		headRaw := p.Repo.File("", path)

		switch {
		case baseRaw == "" && headRaw != "":
			// A config adopted wholesale. Its disables are its starting point,
			// not a retreat from an enforced rule, so nothing is enumerated.
			res.Findings = append(res.Findings, configFinding(path,
				"lint-config-added",
				fmt.Sprintf("this change adds the lint config %s", path),
				""))
			lines = append(lines, "+ "+path)
			continue
		case baseRaw != "" && headRaw == "":
			res.Findings = append(res.Findings, configFinding(path,
				"lint-config-removed",
				fmt.Sprintf("this change removes the lint config %s, so its linter no longer runs as configured", path),
				""))
			lines = append(lines, "- "+path)
			continue
		case baseRaw == headRaw:
			continue
		}

		disabled, parsed := disabledRules(path, baseRaw, headRaw)
		if !parsed {
			id := "lint-config-diff:" + path
			if diff := p.Repo.DiffPath(base.Rev, path); diff != "" {
				res.Evidence[id] = pane.Artifact{Kind: "diff", Content: diff}
			}
			f := configFinding(path,
				"lint-config-changed",
				fmt.Sprintf("this change edits the lint config %s in a format Redline cannot enumerate; read the diff for disabled or downgraded rules", path),
				"")
			f.Evidence = append(f.Evidence, id)
			res.Findings = append(res.Findings, f)
			lines = append(lines, "~ "+path)
			continue
		}
		if len(disabled) == 0 {
			res.Confirmations = append(res.Confirmations, findings.Confirmation{
				Substrate: ConfigSubstrate,
				Rule:      "lint-config-no-rules-off",
				Message:   fmt.Sprintf("%s changed without disabling or downgrading any rule", path),
			})
			lines = append(lines, "~ "+path+" (no rule turned off)")
			continue
		}
		for _, d := range disabled {
			res.Findings = append(res.Findings, configFinding(path,
				"lint-rule-disabled",
				fmt.Sprintf("this change %s in %s", d, path),
				d))
			lines = append(lines, fmt.Sprintf("~ %s: %s", path, d))
		}
	}

	res.Render = pane.Render{
		Title:   "Lint config",
		Summary: fmt.Sprintf("%d lint config drift finding(s)", len(res.Findings)),
		Lines:   lines,
	}
	return res, nil
}

func configFinding(path, rule, message, anchor string) findings.Finding {
	id := path
	if anchor != "" {
		id = path + ":" + anchor
	}
	return findings.Finding{
		File:      path,
		Rule:      rule,
		Substrate: ConfigSubstrate,
		Category:  findings.CategoryLint,
		Severity:  findings.SeverityInfo,
		Message:   message,
		Anchor:    &findings.Anchor{Kind: "lint-config", ID: id},
		Context:   "A rule turned off here stops being enforced for every future change, not just this one.",
	}
}

// disabledRules names what the edit turns off, per format. The second result
// is false when the format cannot be parsed.
func disabledRules(path, baseRaw, headRaw string) ([]string, bool) {
	base := filepath.Base(path)
	switch {
	case base == ".golangci.yml" || base == ".golangci.yaml":
		return golangciDisabled(baseRaw, headRaw)
	case base == ".eslintrc" || base == ".eslintrc.json":
		return eslintDisabled(baseRaw, headRaw)
	case base == ".eslintignore":
		return ignoreAdditions(baseRaw, headRaw), true
	default:
		return nil, false
	}
}

// golangciConfig is the slice of golangci's config this pane reads: what is
// disabled and what is excluded.
type golangciConfig struct {
	Linters struct {
		Disable []string `yaml:"disable"`
		Enable  []string `yaml:"enable"`
	} `yaml:"linters"`
	Issues struct {
		ExcludeRules []struct {
			Linters []string `yaml:"linters"`
			Path    string   `yaml:"path"`
			Text    string   `yaml:"text"`
		} `yaml:"exclude-rules"`
		Exclude []string `yaml:"exclude"`
	} `yaml:"issues"`
}

func golangciDisabled(baseRaw, headRaw string) ([]string, bool) {
	var base, head golangciConfig
	if yaml.Unmarshal([]byte(baseRaw), &base) != nil || yaml.Unmarshal([]byte(headRaw), &head) != nil {
		return nil, false
	}
	var out []string
	for _, l := range newEntries(base.Linters.Disable, head.Linters.Disable) {
		out = append(out, fmt.Sprintf("disables the %s linter", l))
	}
	for _, l := range newEntries(head.Linters.Enable, base.Linters.Enable) {
		// Present in base's enable list and gone from head's: with an explicit
		// enable list, dropping an entry stops running that linter.
		if len(base.Linters.Enable) > 0 {
			out = append(out, fmt.Sprintf("stops enabling the %s linter", l))
		}
	}
	if added := len(head.Issues.ExcludeRules) - len(base.Issues.ExcludeRules); added > 0 {
		out = append(out, fmt.Sprintf("adds %d exclude-rule(s)", added))
	}
	for _, pattern := range newEntries(base.Issues.Exclude, head.Issues.Exclude) {
		out = append(out, fmt.Sprintf("excludes findings matching %q", pattern))
	}
	return out, true
}

// eslintRules reads the rules map of an eslintrc, tolerating both the scalar
// ("off") and the array (["off", {...}]) forms.
func eslintRules(raw string) (map[string]string, bool) {
	var cfg struct {
		Rules map[string]any `json:"rules"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return nil, false
	}
	out := map[string]string{}
	for rule, v := range cfg.Rules {
		out[rule] = eslintLevel(v)
	}
	return out, true
}

func eslintLevel(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		switch int(t) {
		case 0:
			return "off"
		case 1:
			return "warn"
		default:
			return "error"
		}
	case []any:
		if len(t) > 0 {
			return eslintLevel(t[0])
		}
	}
	return ""
}

var eslintRank = map[string]int{"off": 0, "warn": 1, "error": 2}

func eslintDisabled(baseRaw, headRaw string) ([]string, bool) {
	base, ok := eslintRules(baseRaw)
	if !ok {
		return nil, false
	}
	head, ok := eslintRules(headRaw)
	if !ok {
		return nil, false
	}
	var out []string
	var rules []string
	for rule := range head {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	for _, rule := range rules {
		to := head[rule]
		from, existed := base[rule]
		switch {
		case !existed && to == "off":
			out = append(out, fmt.Sprintf("turns the %s rule off", rule))
		case existed && eslintRank[to] < eslintRank[from]:
			out = append(out, fmt.Sprintf("downgrades the %s rule from %s to %s", rule, from, to))
		}
	}
	return out, true
}

// ignoreAdditions reports paths newly ignored by an ignore file.
func ignoreAdditions(baseRaw, headRaw string) []string {
	baseLines := map[string]bool{}
	for _, l := range strings.Split(baseRaw, "\n") {
		baseLines[strings.TrimSpace(l)] = true
	}
	var out []string
	for _, l := range strings.Split(headRaw, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || baseLines[l] {
			continue
		}
		out = append(out, fmt.Sprintf("newly ignores %q", l))
	}
	return out
}

// newEntries returns the entries of head not present in base, in head order.
func newEntries(base, head []string) []string {
	seen := map[string]bool{}
	for _, e := range base {
		seen[e] = true
	}
	var out []string
	for _, e := range head {
		if !seen[e] {
			out = append(out, e)
		}
	}
	return out
}
