// Command redline observes a change and reports evidence about it.
//
// Rung 1 ships the migration-hygiene checks, which need no database and no
// stack bring-up. `redline run` is the deterministic entry point and the
// default; `redline sql` drills into one pane after the fact.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccason/redline/internal/pane"
	"github.com/ccason/redline/internal/report"
	"github.com/ccason/redline/internal/run"
)

const usage = `redline — evidence from execution, not inference from source

usage:
  redline run  [flags]   every applicable pane (the default entry point)
  redline sql  [flags]   the SQL pane alone, for drilling in

flags:
  --base REF        base revision; merge-base with HEAD is used (default: origin/main, else main)
  --upstream REF    branch new migrations must not collide with (default: same as --base)
  --migrations DIR  restrict migration checks to one directory
  --format FMT      report|json (default report; findings JSON is always written to .redline/)
  --out DIR         evidence directory (default .redline)
`

func main() {
	if err := runMain(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "redline:", err)
		os.Exit(1)
	}
}

func runMain(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd := args[0]
	switch cmd {
	case "run", "sql":
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", cmd, usage)
	}

	fs := flag.NewFlagSet("redline "+cmd, flag.ContinueOnError)
	base := fs.String("base", "", "base revision")
	upstream := fs.String("upstream", "", "upstream branch for version-collision checks")
	migDir := fs.String("migrations", "", "migrations directory")
	format := fs.String("format", "report", "report|json")
	out := fs.String("out", ".redline", "evidence directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	res, err := run.Run(run.Options{
		Dir:      cwd,
		Base:     *base,
		Upstream: *upstream,
		MigDir:   *migDir,
	})
	if err != nil {
		return err
	}

	md := report.Markdown(&res.Report, res.Renders, res.Evidence)
	if err := writeEvidence(*out, &res.Report, md, res.Evidence); err != nil {
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res.Report)
	case "report":
		fmt.Print(md)
		return nil
	default:
		return fmt.Errorf("unknown --format %q", *format)
	}
}

// writeEvidence writes findings.json and report.md under the evidence
// directory. findings.json is regenerated every run and fully deterministic;
// comments.json is human state and is never written here, because merging the
// two means every re-run clobbers the reviewer's decisions.
func writeEvidence(dir string, rep any, md string, evidence map[string]pane.Artifact) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "findings.json"), append(buf, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	if len(evidence) == 0 {
		return nil
	}
	evDir := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		return err
	}
	for id, a := range evidence {
		if err := os.WriteFile(filepath.Join(evDir, report.EvidenceFile(id)), []byte(a.Content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
