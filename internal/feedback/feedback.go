// Package feedback reads what a pull request already heard from Redline, and
// what the people reading it said back.
//
// Two runs of the same review on one pull request posted the same defect
// twice, worded differently. Nothing stopped that. `post` skips a finding it
// has already posted, keyed on head SHA plus fingerprint, and a reviewer
// finding's fingerprint is its file plus its normalised wording: the second
// run words the defect differently, and a push changes the SHA, so the key
// never matches across runs. `review` read nothing from the pull request at
// all.
//
// What comes back is worth more than deduplication. When a reader replies
// "same pre-transaction idempotency check as account feed staging", they have
// written down a convention that exists nowhere else, in the one place the
// next review can be shown it. That reply is the cheapest context Redline will
// ever get: it costs nothing, it is specific to this change, and it comes from
// the person whose attention the review is spending.
//
// It is not truth, and the prompt says so. An author dismissing a finding
// about their own code has a stake in the answer, and a dismissal that is
// itself wrong would teach the next review to stay quiet about a real defect.
// So this is carried as the author's position, attributed, and the review is
// told to say which side it takes rather than to obey.
package feedback

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisophus/redline/internal/post"
)

// Thread is one comment Redline left and everything that happened to it.
type Thread struct {
	// Fingerprint is the finding this thread is about, read from the hidden
	// marker `post` wrote into the comment. Empty when the thread is not one
	// of Redline's, which is why those are dropped rather than carried.
	Fingerprint string `json:"fingerprint"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	// Said is what Redline posted, with the markers stripped.
	Said string `json:"said"`
	// Question is the question the finding asked, when it asked one that a
	// lookup could settle. It is how a later review recognises the same claim
	// in different words: the wording moves between runs and the question does
	// not. Empty for a comment posted before the marker existed.
	Question string `json:"question,omitempty"`
	// Replies are what people wrote under it, in order.
	Replies []Reply `json:"replies,omitempty"`
	// Resolved is whether somebody closed the thread. On its own it is a weak
	// signal: it means "fixed" and "dismissed with a click" equally.
	Resolved bool `json:"resolved,omitempty"`
	// Outdated is whether the lines it sits on have since changed. A finding
	// on code that has since moved may have been acted on, and saying so is
	// different from claiming it was.
	Outdated bool `json:"outdated,omitempty"`
	// Up and Down are the reaction counts. The one unambiguous signal on the
	// list, and the cheapest for a reader to give.
	Up   int `json:"up,omitempty"`
	Down int `json:"down,omitempty"`
	// InBody says the finding was written into a review's summary and never
	// had a thread of its own, which is where `post` puts an info finding, a
	// low-confidence one, and one whose line is not in the diff. Nobody can
	// reply to it or close it there, so Replies, Resolved and Outdated are
	// always empty for one.
	InBody bool `json:"inBody,omitempty"`
}

// Reply is one person's answer to a finding.
type Reply struct {
	Author string `json:"author"`
	Body   string `json:"body"`
}

// Answered reports whether somebody responded to the finding at all.
//
// A reply is the answer. Resolution is not required and must not be, which the
// field settled: the team that produced this evidence replies "Not fixing
// (intentional)" on every thread and leaves them open for the merge gate to
// close later. An earlier version of this required both, so it would have
// called every one of those threads unanswered, and a statistic built on it
// would have reported a wall of replies as silence.
//
// Nothing in the review path keys on this. The review is given the reply text
// whether or not a thread is closed, and decides for itself. This is for the
// read-back statistics, where the count is the whole output and a wrong
// predicate is the whole error.
func (t Thread) Answered() bool {
	return len(t.Replies) > 0 || t.Up > 0 || t.Down > 0
}

// Disputed reports whether the response looks like disagreement rather than
// agreement. Loose on purpose, and a caller that needs certainty should read
// the reply text, which is why the review is given it.
func (t Thread) Disputed() bool {
	return t.Down > t.Up
}

// Resolve reads the threads on a pull request. A repository where `gh` cannot
// answer returns an error the caller degrades into a named absence: a review
// that could not read the conversation is different from one where there was
// nothing to read, and only the first is worth telling the reader about.
func Resolve(owner, repo string, number int) ([]Thread, error) {
	if owner == "" || repo == "" || number <= 0 {
		return nil, nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("reading a pull request's own threads needs the gh CLI on PATH: %w", err)
	}
	cmd := exec.Command("gh", "api", "graphql",
		"-f", "query="+threadQuery,
		"-F", "owner="+owner,
		"-F", "repo="+repo,
		"-F", "number="+fmt.Sprint(number))
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if detail := strings.TrimSpace(string(ee.Stderr)); detail != "" {
				return nil, fmt.Errorf("gh api graphql (review threads): %s", detail)
			}
		}
		return nil, fmt.Errorf("gh api graphql (review threads): %w", err)
	}
	return Parse(out)
}

// maxThreads bounds one page of the query. A pull request with more review
// threads than this has a conversation no single review request could carry
// anyway, and paginating to find that out would cost calls to no purpose.
const maxThreads = 100

// threadQuery asks for the whole conversation in one request: the thread's
// state, its comments in order, and their reactions. The REST equivalent is
// three calls and cannot answer whether a thread is resolved at all.
const threadQuery = `query($owner:String!, $repo:String!, $number:Int!) {
  viewer { login }
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      reviews(last:100) {
        nodes { body author { login } }
      }
      reviewThreads(first:100) {
        nodes {
          isResolved
          isOutdated
          path
          line
          comments(first:50) {
            nodes {
              body
              author { login }
              reactionGroups { content reactors { totalCount } }
            }
          }
        }
      }
    }
  }
}`

// wire is the shape the query returns.
type wire struct {
	Data struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
		Repository struct {
			PullRequest struct {
				Reviews struct {
					Nodes []struct {
						Body   string `json:"body"`
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
					} `json:"nodes"`
				} `json:"reviews"`
				ReviewThreads struct {
					Nodes []struct {
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						Line       int    `json:"line"`
						Comments   struct {
							Nodes []struct {
								Body   string `json:"body"`
								Author struct {
									Login string `json:"login"`
								} `json:"author"`
								ReactionGroups []struct {
									Content  string `json:"content"`
									Reactors struct {
										TotalCount int `json:"totalCount"`
									} `json:"reactors"`
								} `json:"reactionGroups"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// Parse reads the query's response into what Redline itself said: the threads
// it started, then the reviewer findings it wrote into a review's summary.
//
// The second half was missing until the saved reviews were laid side by side.
// On the pull requests reviewed more than once, 19 of the 22 findings that
// said an earlier finding over again were repeating one that had only ever
// been in a summary. The next review was never shown those, so it had no way
// to know.
//
// A thread whose first comment carries no Redline marker is somebody else's
// conversation. Carrying those would put a human reviewer's discussion into a
// model's context as though Redline had said it, which is both a
// misattribution and a way to spend the ceiling on a thread about something
// else entirely.
func Parse(raw []byte) ([]Thread, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("reading the review threads: %w", err)
	}
	nodes := w.Data.Repository.PullRequest.ReviewThreads.Nodes
	if len(nodes) > maxThreads {
		nodes = nodes[:maxThreads]
	}
	var out []Thread
	for _, n := range nodes {
		if len(n.Comments.Nodes) == 0 {
			continue
		}
		first := n.Comments.Nodes[0]
		fp, ok := post.FindingFingerprint(first.Body)
		if !ok {
			continue
		}
		t := Thread{
			Fingerprint: fp,
			File:        n.Path,
			Line:        n.Line,
			Said:        post.StripMarkers(first.Body),
			Question:    post.QuestionIn(first.Body),
			Resolved:    n.IsResolved,
			Outdated:    n.IsOutdated,
		}
		for _, g := range first.ReactionGroups {
			switch g.Content {
			case "THUMBS_UP":
				t.Up = g.Reactors.TotalCount
			case "THUMBS_DOWN":
				t.Down = g.Reactors.TotalCount
			}
		}
		for _, c := range n.Comments.Nodes[1:] {
			body := post.StripMarkers(c.Body)
			if body == "" {
				continue
			}
			t.Replies = append(t.Replies, Reply{Author: c.Author.Login, Body: body})
		}
		out = append(out, t)
	}
	said := map[string]bool{}
	for _, t := range out {
		said[t.Fingerprint] = true
	}
	// A summary is read only when the login Redline posts as wrote it. The
	// marker that makes a line a finding is a hidden comment anyone could
	// paste into a review of their own, and a pasted one would be carried as
	// something Redline had said. `post` reads its own markers back under the
	// same rule. The query asks for the newest hundred reviews, since the
	// newest summaries hold the findings a new review is most likely to say
	// again.
	login := postingLogin(w.Data.Viewer.Login)
	for _, r := range w.Data.Repository.PullRequest.Reviews.Nodes {
		if login == "" || !sameLogin(r.Author.Login, login) {
			continue
		}
		for _, t := range inBody(r.Body) {
			if !said[t.Fingerprint] {
				said[t.Fingerprint] = true
				out = append(out, t)
			}
		}
	}
	// Stable order, so a session written twice from the same pull request is
	// the same session and a fixture built from one replays.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out, nil
}

// reviewerRules are the rules a reviewer's own finding is fingerprinted under:
// a plain comment, and one that connects two other findings. A summary also
// lists what the panes measured, and those are measured again on every run,
// so only the reviewer's are carried.
var reviewerRules = map[string]bool{"agent-comment": true, "correlation": true}

// loginEnv names the login Redline posts as, for a token that cannot say.
// `post` reads the same variable.
const loginEnv = "REDLINE_GH_LOGIN"

// postingLogin is the login whose summaries are Redline's own: the one the
// environment names, and otherwise the one the token answers to.
func postingLogin(viewer string) string {
	if login := strings.TrimSpace(os.Getenv(loginEnv)); login != "" {
		return login
	}
	return viewer
}

// sameLogin compares two GitHub logins. GitHub spells a bot's login with a
// "[bot]" suffix in some places and without it in others: REST and
// REDLINE_GH_LOGIN have it, and GraphQL can leave it off an author. Case is
// ignored too, since GitHub logins are not case-sensitive.
func sameLogin(a, b string) bool {
	trim := func(s string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "[bot]")
	}
	return trim(a) != "" && trim(a) == trim(b)
}

// bodyWhereRE matches what `post` writes after a finding it could not put on
// a line: the file and line, or the name of what produced the finding when it
// had no line. It captures what is inside the brackets.
var bodyWhereRE = regexp.MustCompile(`\s*_\(([^()]*)\)_\s*$`)

// bodyLineRE reads the line number out of the file-and-line form.
var bodyLineRE = regexp.MustCompile(`^\S+:(\d+) — `)

// bodyLabelRE matches the "- **Info · agent** — " a summary finding opens with.
var bodyLabelRE = regexp.MustCompile(`^-\s*\*\*[^*]*\*\*\s*—\s*`)

// inBody reads the reviewer findings a review summary carries. Each starts on
// a list line with a label and ends on the line carrying the hidden marker
// `post` wrote. A reviewer's comment can run over several lines, so the two
// are not always the same line. The marker is what makes an entry a finding:
// nothing else in a summary is read.
func inBody(body string) []Thread {
	var out []Thread
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if bodyLabelRE.MatchString(line) {
			start = i
		}
		fp, ok := post.FindingFingerprint(line)
		if !ok {
			continue
		}
		entry := line
		if start >= 0 {
			entry = strings.Join(lines[start:i+1], "\n")
		}
		start = -1
		parts := strings.SplitN(fp, "\x00", 3)
		if len(parts) != 3 || !reviewerRules[parts[1]] {
			continue
		}
		text := post.StripMarkers(entry)
		t := Thread{Fingerprint: fp, File: parts[0], InBody: true}
		if m := bodyWhereRE.FindStringSubmatch(text); m != nil {
			if n := bodyLineRE.FindStringSubmatch(m[1]); n != nil {
				t.Line, _ = strconv.Atoi(n[1])
			}
			text = strings.TrimSpace(text[:len(text)-len(m[0])])
		}
		t.Said = strings.TrimSpace(bodyLabelRE.ReplaceAllString(text, ""))
		if t.Said == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}
