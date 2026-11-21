package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chrisophus/redline/internal/history"
)

// cmdHistory lays every saved review side by side.
//
// It calls nothing. The question it serves is whether a review's fixed
// choices (one call or a split, how many turns of looking) cost anything on
// the reviews that already ran, and that is answerable from what those
// reviews left on disk. --out is the directory holding every session, as it
// is for gc and miss.
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
	if o.format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	_, err = fmt.Print(rep.Render())
	return err
}
