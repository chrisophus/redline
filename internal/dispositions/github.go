package dispositions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxConnection is the page size of every GraphQL connection the query reads. A PR with more than this in any
// one connection is reported as an error rather than silently truncated, since a truncated read would skew
// the disposition rates without anyone noticing.
const maxConnection = 100

// Errors FetchPR returns; each is wrapped with the PR number and detail.
var (
	ErrGraphQL     = errors.New("github graphql error")
	ErrPRNotFound  = errors.New("pull request not found")
	ErrTooManyRows = errors.New("more rows than one page reads")
)

const prQuery = `
query($owner:String!, $name:String!, $number:Int!) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      number state createdAt closedAt mergedAt
      author { __typename }
      commits(first:100) { totalCount nodes { commit { oid committedDate parents { totalCount } } } }
      reviewThreads(first:100) {
        totalCount
        nodes {
          isResolved isOutdated path line originalLine
          comments(first:100) { nodes { body createdAt author { __typename } originalCommit { oid } } }
        }
      }
      reviews(first:100) { totalCount nodes { body } }
      comments(first:100) { totalCount nodes { body } }
    }
  }
}`

// GitHub reads pull requests through the gh CLI, using the developer's own login.
type GitHub struct {
	Owner, Name string
	R           Runner
}

type gqlPR struct {
	Data struct {
		Repository struct {
			PullRequest *struct {
				Number    int       `json:"number"`
				State     string    `json:"state"`
				CreatedAt time.Time `json:"createdAt"`
				ClosedAt  time.Time `json:"closedAt"`
				MergedAt  time.Time `json:"mergedAt"`
				Author    struct {
					Typename string `json:"__typename"`
				} `json:"author"`
				Commits struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						Commit struct {
							OID           string    `json:"oid"`
							CommittedDate time.Time `json:"committedDate"`
							Parents       struct {
								TotalCount int `json:"totalCount"`
							} `json:"parents"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
				ReviewThreads struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						IsResolved   bool   `json:"isResolved"`
						IsOutdated   bool   `json:"isOutdated"`
						Path         string `json:"path"`
						Line         int    `json:"line"`
						OriginalLine int    `json:"originalLine"`
						Comments     struct {
							Nodes []struct {
								Body      string    `json:"body"`
								CreatedAt time.Time `json:"createdAt"`
								Author    struct {
									Typename string `json:"__typename"`
								} `json:"author"`
								OriginalCommit *struct {
									OID string `json:"oid"`
								} `json:"originalCommit"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
				Reviews struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						Body string `json:"body"`
					} `json:"nodes"`
				} `json:"reviews"`
				Comments struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						Body string `json:"body"`
					} `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// FetchPR reads one pull request.
func (g GitHub) FetchPR(ctx context.Context, number int) (PR, error) {
	out, err := g.R.Run(ctx, "gh", "api", "graphql",
		"-f", "query="+prQuery,
		"-F", "owner="+g.Owner, "-F", "name="+g.Name, "-F", "number="+strconv.Itoa(number))
	if err != nil {
		return PR{}, err
	}
	return parsePR(out, number)
}

func parsePR(raw []byte, number int) (PR, error) {
	var resp gqlPR
	if err := json.Unmarshal(raw, &resp); err != nil {
		return PR{}, fmt.Errorf("decode PR %d: %w", number, err)
	}
	if len(resp.Errors) > 0 {
		return PR{}, fmt.Errorf("PR %d: %w: %s", number, ErrGraphQL, resp.Errors[0].Message)
	}
	p := resp.Data.Repository.PullRequest
	if p == nil {
		return PR{}, fmt.Errorf("PR %d: %w", number, ErrPRNotFound)
	}
	for name, c := range map[string][2]int{
		"commits": {p.Commits.TotalCount, len(p.Commits.Nodes)}, "review threads": {p.ReviewThreads.TotalCount, len(p.ReviewThreads.Nodes)},
		"reviews": {p.Reviews.TotalCount, len(p.Reviews.Nodes)}, "comments": {p.Comments.TotalCount, len(p.Comments.Nodes)},
	} {
		if c[0] > maxConnection {
			return PR{}, fmt.Errorf("PR %d has %d %s, more than the %d one page reads: %w", number, c[0], name, maxConnection, ErrTooManyRows)
		}
	}

	pr := PR{Number: p.Number, State: p.State, CreatedAt: p.CreatedAt, AuthorIsBot: p.Author.Typename == "Bot"}
	pr.ClosedAt = p.ClosedAt
	if !p.MergedAt.IsZero() {
		pr.ClosedAt = p.MergedAt
	}
	for _, n := range p.Commits.Nodes {
		pr.Commits = append(pr.Commits, Commit{SHA: strings.ToLower(n.Commit.OID), Time: n.Commit.CommittedDate, IsMerge: n.Commit.Parents.TotalCount > 1})
	}
	for _, n := range p.ReviewThreads.Nodes {
		th := Thread{Path: n.Path, Line: n.Line, OrigLine: n.OriginalLine, IsResolved: n.IsResolved, IsOutdated: n.IsOutdated}
		for _, c := range n.Comments.Nodes {
			tc := ThreadComment{Body: c.Body, CreatedAt: c.CreatedAt, AuthorIsBot: c.Author.Typename != "" && c.Author.Typename != "User"}
			if c.OriginalCommit != nil {
				tc.CommitSHA = strings.ToLower(c.OriginalCommit.OID)
			}
			th.Comments = append(th.Comments, tc)
		}
		pr.Threads = append(pr.Threads, th)
	}
	for _, n := range p.Reviews.Nodes {
		pr.ReviewBodies = append(pr.ReviewBodies, n.Body)
	}
	for _, n := range p.Comments.Nodes {
		pr.IssueComments = append(pr.IssueComments, n.Body)
	}
	return pr, nil
}

// FetchRecentPRNumbers lists the numbers of the most recently updated pull requests in any state.
func (g GitHub) FetchRecentPRNumbers(ctx context.Context, limit int) ([]int, error) {
	out, err := g.R.Run(ctx, "gh", "pr", "list", "-R", g.Owner+"/"+g.Name, "--state", "all",
		"--search", "sort:updated-desc", "--limit", strconv.Itoa(limit), "--json", "number")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("decode pr list: %w", err)
	}
	nums := make([]int, len(rows))
	for i, r := range rows {
		nums[i] = r.Number
	}
	return nums, nil
}
