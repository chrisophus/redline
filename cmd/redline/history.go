package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/chrisophus/redline/internal/history"
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
// of history that costs money, so it says how many questions it is sending
// and to what before the first one goes.
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
