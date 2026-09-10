// Package instructions reads the review rules a repository writes into its
// own configuration, including the ones it learned from being reviewed.
//
// Two things a team knows that no file in the repository states. The first is
// what it wants a reviewer to pay attention to in a particular directory:
// input validation under the API handlers, migration safety under the schema.
// Every shipped review tool has a place to put that, because a reviewer told
// nothing reviews everything equally.
//
// The second is the more interesting one, and it is why this exists rather
// than a line in AGENTS.md. When somebody replies to a finding to say it is
// deliberate, they have stated a convention that was written down nowhere.
// Field use produced a review whose every finding was dismissed that way, and
// every one of those replies was a rule the next review needed and could not
// have known. Writing it here is how it gets known.
//
// A learned rule carries where it came from, and it ranks below a rule the
// team wrote deliberately. That ordering is the guard. A file of learnings
// grows from moments when somebody was defending their own change, so it can
// acquire a line that is simply wrong, and it must never be able to overrule
// what the team sat down and decided. It also cannot suppress a finding by
// itself: it reaches the review as context, and the verifying pass has to say
// out loud that it ruled on a finding because of it.
package instructions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/envelope"
	"github.com/chrisophus/redline/internal/globmatch"
	"gopkg.in/yaml.v3"
)

// ProviderName is what the report and the prompt call this context.
const ProviderName = "review-rules"

// RoleGuideline is the role these arrive under, the same one the repository's
// instruction files use. They are the same kind of thing to a reader: what
// this team has said its code should look like.
const RoleGuideline = envelope.Role("guideline")

// Priorities. Both sit at or below what houserules gives a written rule, so
// when the budget binds, a rule the team wrote in a file survives a rule
// somebody typed into a config, and both survive a learned one.
const (
	writtenPriority = 75
	learnedPriority = 60
)

var configNames = []string{".redline.yml", ".redline.yaml"}

// maxRules bounds what one review carries. A learnings file grows by a line
// every time somebody dismisses a finding, so it grows without limit and
// without anyone deciding to let it; the bound is what stops a year of them
// from being most of a review request.
const maxRules = 25

// Rule is one instruction, from the config or learned from a review.
type Rule struct {
	// Scope are the globs it applies to. Empty means every change.
	Scope []string `yaml:"scope"`
	// Text is the rule itself, in the words of whoever wrote it.
	Text string `yaml:"text"`
	// LearnedFrom is the thread this came out of, when it came out of one. Its
	// presence is what makes a rule a learning rather than a decision, and its
	// content is how a reader checks the rule against what was actually said.
	//
	// A learning with no link is still a learning; the field is what a person
	// pastes so the next reader can go and look. Requiring it would only
	// produce empty strings.
	LearnedFrom string `yaml:"learned_from"`
}

type fileConfig struct {
	Review struct {
		Instructions []Rule `yaml:"instructions"`
	} `yaml:"review"`
}

// Load reads the rules from .redline.yml. A repository with none returns
// nothing and no error.
func Load(root string) ([]Rule, error) {
	for _, name := range configNames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var cfg fileConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var out []Rule
		for i, r := range cfg.Review.Instructions {
			r.Text = strings.TrimSpace(r.Text)
			if r.Text == "" {
				// A rule with no words is not a rule. Carrying it would spend
				// budget on a heading and make the report claim a rule was
				// read.
				return nil, fmt.Errorf("%s: review.instructions[%d] has no text", name, i)
			}
			out = append(out, r)
		}
		return out, nil
	}
	return nil, nil
}

// Resolve turns the rules that govern this change into an envelope.
func Resolve(root string, changed []string) (*envelope.Envelope, error) {
	rules, err := Load(root)
	if err != nil {
		return nil, err
	}
	var kept []Rule
	for _, r := range rules {
		if len(r.Scope) > 0 && !matchesAny(r.Scope, changed) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == 0 {
		return nil, nil
	}
	// Written rules first, learned ones after, so a reader meets what the team
	// decided before what it worked out from a thread, and the bound below
	// drops the learned ones first.
	sort.SliceStable(kept, func(i, j int) bool {
		return (kept[i].LearnedFrom == "") && (kept[j].LearnedFrom != "")
	})
	omitted := 0
	if len(kept) > maxRules {
		omitted = len(kept) - maxRules
		kept = kept[:maxRules]
	}

	env := &envelope.Envelope{
		SchemaVersion:  envelope.SchemaVersion,
		Provider:       envelope.Provider{Name: ProviderName, Version: "builtin"},
		PromptFragment: promptFragment,
	}
	for _, r := range kept {
		scope := "every change"
		if len(r.Scope) > 0 {
			scope = strings.Join(r.Scope, ", ")
		}
		symbol := "a review rule for " + scope
		priority := writtenPriority
		details := map[string]string{"scope": scope, "source": "configured"}
		if r.LearnedFrom != "" {
			symbol = "learned from a review, for " + scope
			priority = learnedPriority
			details["source"] = "learned"
			details["learnedFrom"] = r.LearnedFrom
		}
		env.Expansions = append(env.Expansions, envelope.Expansion{
			Role:     RoleGuideline,
			Priority: priority,
			Symbol:   symbol,
			// No file: this rule is not quoted from one, and pointing at
			// .redline.yml would invite a reader to look for it at a line
			// number that means nothing.
			Content: r.Text,
			Details: details,
		})
	}
	if omitted > 0 {
		env.Notes = append(env.Notes, fmt.Sprintf(
			"%d further review rule(s) apply to this change and are not carried; "+
				"the ones the team wrote were kept over the ones learned from a thread", omitted))
	}
	return env, nil
}

func matchesAny(globs, changed []string) bool {
	for _, p := range changed {
		if globmatch.MatchesAny(globs, strings.TrimPrefix(filepath.ToSlash(p), "./")) {
			return true
		}
	}
	return false
}

// promptFragment says what these are and, for the learned ones, where they
// came from. The difference matters: a rule the team wrote is a decision, and
// a rule learned from a thread is one person's account of a decision, recorded
// while they were defending their own change.
const promptFragment = `The context tagged review-rules is what this repository
has told its reviewer, from its own configuration rather than from a file in
the tree. It is not code and it is not part of the change.

A block marked configured is a rule the team wrote for reviews of these paths.
Treat it the way you treat the repository's own instruction files: binding, and
not a checklist to audit the change against.

A block marked learned is different, and the difference is worth holding on to.
It was written down after somebody replied to a finding on a pull request to
say the thing was deliberate. That makes it good evidence about what this team
does, and it makes it one person's account, recorded while they were explaining
their own change. So: do not raise a finding a learned rule already answers,
because the author has answered it once and will not enjoy answering it again.
But if what you can see in this change contradicts a learned rule, say so, once,
naming what you saw. A rule learned from a thread never outranks a rule the
team wrote in a file, and neither outranks what is plainly in the diff.`
