package review

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/chrisophus/redline/internal/findings"
)

// A review is not a stable function of its input, and the variance is total.
// Measured on a fixture carrying fourteen labelled defects: across forty-odd
// samples in eleven configurations, no finding was ever produced by two
// samples. Recall went 3 of 14 at one sample, 6 at nine, 7 at three under a
// better prompt; the samples were disjoint every time.
//
// Two consequences follow, and only one of them is the obvious one.
//
// The union is additive. Sampling k times and keeping everything found is
// worth roughly k times the findings, which is why Samples exists at all.
//
// Agreement cannot be a confidence signal. Deriving confidence from how many
// samples agreed was the first design here and it is unimplementable: with no
// overlap every finding scores one out of k, so the report's low-confidence
// fold would hide all of them. Confidence stays the model's own claim.
//
// That second conclusion is narrower than it reads, and the narrowing is worth
// stating because other reviewers do vote across passes and report it works.
// The measurement was taken against an identity that was the finding's own
// prose. Two samples describing one defect in different words counted as two
// findings under it, so total disagreement is what it had to find whether or
// not the samples agreed about anything. UnionKey now prefers the question a
// finding asks, which is the same sentence from both samples where the prose
// never is, and the measurement is worth taking again under it. Until it is,
// the conclusion above stands as what was actually observed.

// runSamples takes Samples independent reviews and unions them. They share no
// state, so with the cache off they go out together and the wall clock is one
// review's. With it on, the first goes alone and primes the cache, and the rest
// go out together as soon as its stream carries content: the endpoint cannot
// send content before it has read the prompt, so by then the entry is written
// and the rest pay the cache-read rate for the prompt instead of each paying to
// read it. They also go when the first ends without content, failed or not, so
// a primer that breaks costs the rest nothing but the wait.
func runSamples(ctx context.Context, in Input, opts Options, first *Result) (*Result, error) {
	type sample struct {
		res *Result
		err error
	}
	out := make([]sample, opts.Samples)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var landed int
	// launch starts sample i. A non-nil primed is closed once that sample's
	// stream carries content, or when its call ends without any.
	launch := func(i int, primed chan struct{}) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each sample is a fresh call over the same assembled prompt.
			// One sample's options are the single-sample options, so nothing
			// below this point knows it is one of many.
			one := opts
			one.Samples = 1
			release := func() {}
			if primed != nil {
				var once sync.Once
				release = func() { once.Do(func() { close(primed) }) }
				one.onOutput = release
			}
			res, err := runOnce(ctx, in, one, first.clone())
			release()
			out[i] = sample{res: res, err: err}
			if opts.Progress == nil {
				return
			}
			// Reported in the order they land rather than by index, and
			// serialised, because the point is only to say that something
			// arrived: several minutes of silence for a run that is working
			// looks the same as one that has hung.
			mu.Lock()
			defer mu.Unlock()
			landed++
			if err != nil {
				opts.Progress(fmt.Sprintf("sample %d of %d failed: %v", landed, opts.Samples, err))
				return
			}
			opts.Progress(fmt.Sprintf("sample %d of %d: %d finding(s), %s",
				landed, opts.Samples, len(res.Review.Comments),
				FormatCost(res.CostUSD, res.CostKnown)))
		}()
	}
	if opts.cacheOn() {
		primed := make(chan struct{})
		launch(0, primed)
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("sample 1 of %d goes first; the other %d follow once it has read the prompt, "+
				"so they read it from the cache", opts.Samples, opts.Samples-1))
		}
		select {
		case <-primed:
		case <-ctx.Done():
		}
		for i := 1; i < opts.Samples; i++ {
			launch(i, nil)
		}
	} else {
		for i := range opts.Samples {
			launch(i, nil)
		}
	}
	wg.Wait()

	merged := first.clone()
	merged.Samples = opts.Samples
	var kept []*Result
	var failures []string
	for _, s := range out {
		if s.res != nil {
			merged.Usage.InputTokens += s.res.Usage.InputTokens
			merged.Usage.OutputTokens += s.res.Usage.OutputTokens
			merged.Usage.CacheReadTokens += s.res.Usage.CacheReadTokens
			merged.Usage.CacheWriteTokens += s.res.Usage.CacheWriteTokens
			merged.Usage.ThinkingTokens += s.res.Usage.ThinkingTokens
			if s.res.Duration > merged.Duration {
				merged.Duration = s.res.Duration
			}
		}
		if s.err != nil {
			failures = append(failures, s.err.Error())
			continue
		}
		merged.Stubs += s.res.Stubs
		kept = append(kept, s.res)
	}
	merged.CostUSD, merged.CostKnown = merged.Usage.Cost(opts.Model)
	merged.SamplesFailed = len(failures)
	merged.Turns = len(kept)

	if len(kept) == 0 {
		// Every sample failed, so there is no review. The usage is still on
		// the result: the calls were paid for whether or not they answered.
		return merged, fmt.Errorf("all %d samples failed: %s", opts.Samples, failures[0])
	}
	merged.Review = unionReviews(kept)
	merged.StopReason = kept[0].StopReason
	merged.ServedModel = kept[0].ServedModel
	return merged, nil
}

// clone copies the assembled half of a result, which every sample shares, and
// leaves the answered half zero.
func (r *Result) clone() *Result {
	c := *r
	c.Review = findings.Review{}
	c.Usage = Usage{}
	c.CostUSD, c.CostKnown = 0, false
	c.StopReason = ""
	c.Duration = 0
	c.Turns = 0
	c.RulingOutputTokens = 0
	c.ScoutCostUSD = 0
	c.Stubs = 0
	// A clone is a new call, and a new call continues no conversation unless
	// runStepwise attaches one. The ruling clones whatever result it is handed,
	// and a stepwise turn 2 carried into it would send the ruling as a third
	// turn of a conversation it was never priced as.
	c.history, c.answering = nil, nil
	c.reply, c.replyToolUses = anthropic.MessageParam{}, nil
	return &c
}

// unionReviews merges the samples' comments, keeping the first phrasing of
// each remark and the longest prose.
//
// Two samples that found the same defect describe it differently, so identity
// is the file plus the message with its digits collapsed -- the same normalise
// the report's own fingerprint uses, for the same reason: "line 12" and "line
// 947" are one remark, not two.
func unionReviews(samples []*Result) findings.Review {
	var out findings.Review
	byKey := map[string]int{}
	files := map[string]string{}
	for _, s := range samples {
		rev := s.Review
		if len(rev.Overview) > len(out.Overview) {
			out.Overview = rev.Overview
		}
		for path, summary := range rev.Files {
			if len(summary) > len(files[path]) {
				files[path] = summary
			}
		}
		for _, c := range rev.Comments {
			key := UnionKey(c)
			if i, ok := byKey[key]; ok {
				if len(c.Body) > len(out.Comments[i].Body) {
					out.Comments[i].Body = c.Body
				}
				continue
			}
			byKey[key] = len(out.Comments)
			out.Comments = append(out.Comments, c)
		}
	}
	if len(files) > 0 {
		out.Files = files
	}
	return out
}

// UnionKey is what makes two samples' comments one finding.
//
// The question first, when it distinguishes anything. Two samples that found
// one defect describe it differently and ask the same thing about it: "does
// this repository already do this, look up awsOfferFeedRawColumns" is the same
// sentence from both, where the prose never is. That is also why the
// zero-overlap measurement recorded above is worth taking again under this
// key: it was made against prose, so it could only ever have found what prose
// can match.
//
// The prose remains the fallback, for a comment whose question is absent, is
// "none", or names no subject to compare. A hand-written review has no
// question at all and must keep merging the way it always did.
//
// Exported because the eval unions samples too, to score the review a reader
// would actually be handed. Two implementations of this would be two different
// reviews, one measured and one shipped.
func UnionKey(c findings.ReviewComment) string {
	file := strings.ToLower(strings.TrimSpace(c.File))
	if k := c.Question.Key(); k != "" {
		return file + "\x00q\x00" + k
	}
	return file + "\x00" +
		findings.NormalizeMessage(strings.ToLower(strings.TrimSpace(c.Body)))
}
