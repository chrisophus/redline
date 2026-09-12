package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The cost target is an average, not a cap on every review.
//
// This matters for what the ceiling is for. If every review had to come in
// under the target, the ceiling would be a governor and it would trim context
// from exactly the large changes that most need it. Because the target is an
// average, a large change is allowed to cost more than a small one, and the
// ceiling's job is only to stop the pathological tail.
//
// An average is a claim about a distribution, and a distribution has to be
// measured. So every review appends one line here, and the numbers are read
// back rather than asserted.

// ledgerFile is where per-review cost lands, beside the report it produced.
const ledgerFile = "reviews.jsonl"

// Entry is one review's cost record.
type Entry struct {
	At       time.Time `json:"at"`
	API      string    `json:"api,omitempty"`
	Model    string    `json:"model"`
	Effort   string    `json:"effort,omitempty"`
	Usage    Usage     `json:"usage"`
	CostUSD  float64   `json:"costUSD"`
	Known    bool      `json:"costKnown"`
	Seconds  float64   `json:"seconds"`
	Findings int       `json:"findings"`
	// StopReason is what ended the last turn, and Truncated whether that was
	// the output cap. Recorded because a review that found nothing and a
	// review that was cut off mid-finding cost the same and look identical
	// here without them: the design expects roughly three changes in ten to
	// deserve no comment, so a zero-finding line is only readable as a clean
	// pass once the cap can be ruled out.
	StopReason string `json:"stopReason,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	// Batched marks a row billed at the Message Batches tier's half rate.
	// Summarize leaves those out of the distribution: the target is a claim
	// about what a review costs the person waiting for it, and a sweep of
	// half-price fixtures would drag that average somewhere no interactive
	// run can reach.
	Batched bool `json:"batched,omitempty"`
	// Ceiling and InputEstimate record what the request was allowed and what
	// it used, so a run that was trimmed can be told from one that fit.
	Ceiling       int  `json:"ceiling"`
	InputEstimate int  `json:"inputEstimate"`
	OverCeiling   bool `json:"overCeiling,omitempty"`
	// Samples is how many independent reviews this line paid for, and
	// SamplesFailed how many of them did not answer. A mean cost across
	// lines is only comparable when it says so: three samples cost three
	// reviews and record as one line.
	Samples       int `json:"samples,omitempty"`
	SamplesFailed int `json:"samplesFailed,omitempty"`
	// RulingOutputTokens is what the verifying pass wrote, and ScoutCostUSD
	// what stage two's lookups cost. Kept apart from Usage and named on their
	// own so a run that checked its findings can be told from one that did
	// not, and so the review's own output median is not inflated by the
	// ruling's handful of objects.
	RulingOutputTokens int64   `json:"rulingOutputTokens,omitempty"`
	ScoutCostUSD       float64 `json:"scoutCostUSD,omitempty"`
}

// Record appends one review to the ledger in dir. Failing to write it is not
// fatal: a review that produced findings has done its job, and losing a cost
// line is not worth failing the command over. The error is returned so the
// caller can say so.
func Record(dir string, r *Result, effort string) error {
	if r == nil {
		return nil
	}
	e := Entry{
		At: time.Now().UTC(), API: r.API, Model: r.Model, Effort: effort,
		Usage: r.Usage, CostUSD: r.CostUSD, Known: r.CostKnown,
		Seconds: r.Duration.Seconds(), Findings: len(r.Review.Comments),
		StopReason: r.StopReason, Truncated: r.Truncated,
		Batched: r.Batched,
		Ceiling: r.Ceiling, InputEstimate: r.InputEstimate, OverCeiling: r.OverCeiling,
		Samples: r.Samples, SamplesFailed: r.SamplesFailed,
		RulingOutputTokens: r.RulingOutputTokens, ScoutCostUSD: r.ScoutCostUSD,
	}
	buf, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 0600: the ledger records what this developer's reviews cost, which is
	// nobody else's business on a shared machine.
	f, err := os.OpenFile(filepath.Join(dir, ledgerFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(buf, '\n'))
	return err
}

// ReadLedger loads every recorded review from dir. A missing file is not an
// error: no reviews have run yet.
func ReadLedger(dir string) ([]Entry, error) {
	buf, err := os.ReadFile(filepath.Join(dir, ledgerFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	dec := json.NewDecoder(bytes.NewReader(buf))
	for dec.More() {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			return out, fmt.Errorf("%s: %w", ledgerFile, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Stats is the distribution the target is a claim about.
type Stats struct {
	Count  int
	Mean   float64
	Median float64
	P90    float64
	Max    float64
	Min    float64
	// Unknown counts reviews whose model had no rate. They are excluded from
	// every number above, and named, because folding them in as zero would
	// drag the average down with reviews nobody priced.
	Unknown int
	// MeanSeconds is wall time, the other number a pre-push tool is judged on.
	MeanSeconds float64
	// Batched counts rows excluded for having gone over the batch tier.
	Batched int
	// Truncated counts reviews the output cap cut off. They are paid for in
	// full and yield nothing, so they belong beside the average rather than
	// inside it: a rising count is the signal to raise --max-tokens, and it
	// is the one thing that separates a cheap run from a wasted one.
	Truncated int
	// MedianOutput is what reviews actually emitted. It replaces the guess
	// in ExpectedOutputTokens once there is enough evidence to have a
	// median, so the estimate converges on this installation's own reviews
	// rather than staying at a constant somebody picked.
	MedianOutput int64
}

// minForMedianOutput is how many recorded reviews it takes before the measured
// median is trusted over the default. Three is enough to stop one unusually
// long review from setting the estimate for every later one.
const minForMedianOutput = 3

// ExpectedOutput returns the output size to price the next review at: the
// measured median when there is one, and the documented default before that.
func (s Stats) ExpectedOutput() int64 {
	if s.Count >= minForMedianOutput && s.MedianOutput > 0 {
		return s.MedianOutput
	}
	return ExpectedOutputTokens
}

// Summarize computes the distribution.
func Summarize(entries []Entry) Stats {
	var s Stats
	var costs []float64
	var outs []int64
	var secs float64
	for _, e := range entries {
		if e.Batched {
			s.Batched++
			continue
		}
		if !e.Known {
			s.Unknown++
			continue
		}
		costs = append(costs, e.CostUSD)
		secs += e.Seconds
		if e.Truncated {
			s.Truncated++
		}
		if e.Usage.OutputTokens > 0 {
			outs = append(outs, e.Usage.OutputTokens)
		}
	}
	if len(outs) > 0 {
		sort.Slice(outs, func(i, j int) bool { return outs[i] < outs[j] })
		s.MedianOutput = outs[len(outs)/2]
	}
	s.Count = len(costs)
	if s.Count == 0 {
		return s
	}
	sort.Float64s(costs)
	var sum float64
	for _, c := range costs {
		sum += c
	}
	s.Mean = sum / float64(s.Count)
	s.Median = percentile(costs, 50)
	s.P90 = percentile(costs, 90)
	s.Min = costs[0]
	s.Max = costs[s.Count-1]
	s.MeanSeconds = secs / float64(s.Count)
	return s
}

// percentile is the nearest-rank value at pct of a sorted slice: index
// ceil(pct*n/100)-1.
//
// The naive (n*pct)/100 is one rank too high, and biased the same way at
// every size: at two reviews the "median" was the more expensive of the two,
// and for any n up to ten the "p90" was the maximum, so the tail statistic
// that exists to show one outlier beside the average was that outlier. The
// clamp is for pct at the ends, not for the arithmetic; ceil(pct*n/100)-1 is
// within range for every n >= 1.
func percentile(sorted []float64, pct int) float64 {
	idx := (len(sorted)*pct+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// String renders the distribution for a terminal. The average leads because
// that is the number the target is about; the tail is beside it because an
// average alone hides a review that cost ten times the rest.
func (s Stats) String() string {
	if s.Count == 0 {
		if s.Unknown > 0 {
			return fmt.Sprintf("%d review(s) recorded, none with a known rate", s.Unknown)
		}
		return "no reviews recorded yet"
	}
	out := fmt.Sprintf("%d review(s): mean $%.4f, median $%.4f, p90 $%.4f, range $%.4f to $%.4f, mean wall %.1fs",
		s.Count, s.Mean, s.Median, s.P90, s.Min, s.Max, s.MeanSeconds)
	if s.Batched > 0 {
		out += fmt.Sprintf(" (%d more ran at the batch tier's half rate and are excluded)", s.Batched)
	}
	if s.Truncated > 0 {
		out += fmt.Sprintf("; %d hit the output cap and were paid for in full for nothing", s.Truncated)
	}
	if s.Unknown > 0 {
		out += fmt.Sprintf(" (%d more had no rate and are excluded)", s.Unknown)
	}
	return out
}
