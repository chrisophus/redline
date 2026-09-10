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
	"os/exec"
	"sort"
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
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
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
		Repository struct {
			PullRequest struct {
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

// Parse reads the query's response into the threads Redline itself started.
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
