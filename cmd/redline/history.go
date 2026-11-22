package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/chrisophus/redline/internal/history"
	"github.com/chrisophus/redline/internal/jev"
	"github.com/chrisophus/redline/internal/miss"
	"github.com/chrisophus/redline/internal/review"
)

// cmdHistory lays every saved review side by side.
//
// Without --ask it calls nothing. The question it serves is whether a
// review's fixed choices (one call or a split, how many turns of looking)
// cost anything on the reviews that already ran, and that is answerable from
// what those reviews left on disk. --ask then puts the same choices to a
// model, to see what it would have done differently. --out is the directory
// holding every session, as it is for gc and miss.
func cmdHistory(o opts) error {
	if o.format != "report" && o.format != "json" {
		return fmt.Errorf("--format %q: use report or json", o.format)
	}
	ledger := o.jsonl
	if ledger == "" {
		ledger = o.out
	}
	rep, err := history.Collect(o.out, ledger)
	if err != nil {
		return err
	}
	if o.ask {
		if err := askHistory(o, rep); err != nil {
			return err
		}
	}
	if o.repeats {
		client, err := jev.New("", "", os.Getenv(jev.KeyEnv))
		if err != nil {
			return fmt.Errorf("--repeats: %w", err)
		}
		judge := &jev.Repeats{Client: client}
		rep.ReplayRepeats(context.Background(), judge, func(done, total int) {
			if done%20 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "redline: %d of %d later reviews replayed\n", done, total)
			}
		})
		usage, calls := judge.Spent()
		fmt.Fprintf(os.Stderr, "redline: the replay sent %d request(s) to %s for $%.4f\n", calls, client.Model, usage.CostUSD())
	}
	if o.replayMisses || o.replayEvidence || o.replayContext {
		if err := judgeHistory(o, rep, ledger); err != nil {
			return err
		}
	}
	if o.format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	_, err = fmt.Print(rep.Render())
	return err
}

// askHistory puts the past reviews' choices to a model. This is the one part
// of history that costs money, so it says which model it is asking and about
// how many reviews before the first question goes.
func askHistory(o opts, rep *history.Report) error {
	e := history.Endpoint{API: o.api, Model: o.model, BaseURL: o.baseURL, APIUser: o.apiUser}
	switch e.API {
	case history.APIJev:
		e.APIKey = os.Getenv("TYPESAFE_API_KEY")
		if e.Model == "" {
			e.Model = history.DefaultJevModel
		}
	case review.APIOpenAI:
		e.APIKey = os.Getenv("OPENAI_API_KEY")
		if e.BaseURL == "" {
			e.BaseURL = os.Getenv("OPENAI_BASE_URL")
		}
		if e.Model == "" {
			e.Model = review.DefaultOpenAIModel
		}
	default:
		if e.Model == "" {
			e.Model = review.DefaultModel
		}
	}
	asker, err := history.NewAsker(e)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "redline: asking %s about %d saved review(s)\n", e.Model, len(rep.Runs))
	return rep.Ask(context.Background(), asker, e.Model, o.maxAsks, func(done, total int) {
		if done%25 == 0 || done == total {
			fmt.Fprintf(os.Stderr, "redline: %d of %d questions answered\n", done, total)
		}
	})
}

// judgeHistory runs the replays that put a yes/no question to Jev about what
// a saved review already holds. They share one client, and each says what it
// spent.
func judgeHistory(o opts, rep *history.Report, ledger string) error {
	client, err := jev.New("", "", os.Getenv(jev.KeyEnv))
	if err != nil {
		return fmt.Errorf("--misses, --evidence and --context ask TypeSafe's Jev: %w", err)
	}
	ctx := context.Background()
	spent := func(what string, usage jev.Usage, calls int) {
		fmt.Fprintf(os.Stderr, "redline: %s sent %d request(s) to %s for $%.4f\n", what, calls, client.Model, usage.CostUSD())
	}
	if o.replayMisses {
		misses, err := miss.Read(ledger)
		if err != nil {
			return fmt.Errorf("--misses: %w", err)
		}
		judge := &jev.Misses{Client: client}
		rep.ReplayMisses(ctx, judge, misses, nil)
		usage, calls := judge.Spent()
		spent("--misses", usage, calls)
	}
	if o.replayEvidence {
		judge := &jev.Evidence{Client: client}
		rep.ReplayEvidence(ctx, judge, progressEvery(20, "reviews checked for evidence"))
		usage, calls := judge.Spent()
		spent("--evidence", usage, calls)
	}
	if o.replayContext {
		judge := &jev.Context{Client: client}
		rep.ReplayContext(ctx, judge, progressEvery(10, "reviews checked for context"))
		usage, calls := judge.Spent()
		spent("--context", usage, calls)
	}
	return nil
}

// progressEvery reports on stderr after every n reviews and after the last.
func progressEvery(n int, what string) func(done, total int) {
	return func(done, total int) {
		if done%n == 0 || done == total {
			fmt.Fprintf(os.Stderr, "redline: %d of %d %s\n", done, total, what)
		}
	}
}
