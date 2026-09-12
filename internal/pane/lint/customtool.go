package lint

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// decodeOneJSON reads a single JSON value from s and ignores anything after
// it. Some tools (golangci-lint v2 among them) print their JSON and then an
// unconditional "N issues." summary line to the same stream; a Decoder reads
// the value at the start, where json.Unmarshal would reject the whole body.
func decodeOneJSON(s string, v any) error {
	return json.NewDecoder(strings.NewReader(s)).Decode(v)
}

// runConfiguredLinter runs a linter-kind configured tool at one revision's
// directory and returns its issues, parsed by the tool's declared Format.
func runConfiguredLinter(dir string, cfg ToolConfig) ([]Issue, error) {
	stdout, stderr, exit, err := runTool(dir, cfg.Command, cfg.Args...)
	if err != nil {
		return nil, err
	}
	if !exitOK(exit, cfg.OKExitCodes) {
		return nil, fmt.Errorf("%s exited %d: %s", cfg.Name, exit, firstLine(stderr))
	}
	return parseToolOutput(stdout, cfg)
}

// parseToolOutput turns a tool's stdout into issues by its Format, then
// applies the configured line offset uniformly.
func parseToolOutput(stdout string, cfg ToolConfig) ([]Issue, error) {
	var (
		issues []Issue
		err    error
	)
	switch cfg.Format {
	case "sarif":
		issues, err = parseSARIF(stdout, cfg)
	case "spectral":
		issues, err = parseSpectral(stdout, cfg)
	case "json", "":
		issues, err = parseGenericJSON(stdout, cfg)
	default:
		return nil, fmt.Errorf("%s: unknown format %q (want sarif, spectral, or json)", cfg.Name, cfg.Format)
	}
	if err != nil {
		return nil, err
	}
	if cfg.LineOffset != 0 {
		for i := range issues {
			issues[i].Line += cfg.LineOffset
		}
	}
	return issues, nil
}

// exitOK reports whether a tool's exit code means it ran. The default, for a
// tool that does not declare its codes, is the universal linter convention:
// 0 is clean, 1 is issues found, anything else is the tool itself failing.
func exitOK(exit int, okCodes []int) bool {
	if len(okCodes) == 0 {
		return exit == 0 || exit == 1
	}
	for _, c := range okCodes {
		if c == exit {
			return true
		}
	}
	return false
}

// parseGenericJSON reads an arbitrary JSON body using the tool's ResultsPath and
// Fields to locate each result's file, line, rule, message and severity. When
// ItemsPath is set, each ResultsPath element is a group whose inner array fans
// out into one Issue per item, with fields resolved against the item then the
// group.
func parseGenericJSON(stdout string, cfg ToolConfig) ([]Issue, error) {
	var root any
	if err := decodeOneJSON(stdout, &root); err != nil {
		return nil, fmt.Errorf("%s output was not JSON: %w: %s", cfg.Name, err, firstLine(stdout))
	}
	results, ok := jsonPathGet(root, cfg.ResultsPath)
	if !ok {
		return nil, fmt.Errorf("%s: resultsPath %q not found in output", cfg.Name, cfg.ResultsPath)
	}
	arr, ok := results.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: resultsPath %q is not an array", cfg.Name, cfg.ResultsPath)
	}
	var out []Issue
	for _, item := range arr {
		if cfg.ItemsPath == "" {
			out = append(out, issueFrom(item, nil, cfg))
			continue
		}
		inner, ok := jsonPathGet(item, cfg.ItemsPath)
		if !ok {
			// A group with no inner array (a clean file) contributes nothing.
			continue
		}
		items, ok := inner.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: itemsPath %q is not an array", cfg.Name, cfg.ItemsPath)
		}
		for _, sub := range items {
			out = append(out, issueFrom(sub, item, cfg))
		}
	}
	return out, nil
}

// issueFrom builds one Issue, resolving each field against item and falling back
// to parent when item does not carry it. With itemsPath fan-out the file often
// lives on the outer group and the line, rule, and message on the inner item;
// the fallback covers that without a separate outer and inner mapping.
func issueFrom(item, parent any, cfg ToolConfig) Issue {
	get := func(path string) (any, bool) {
		if path == "" {
			return nil, false
		}
		if v, ok := jsonPathGet(item, path); ok {
			return v, true
		}
		if parent != nil {
			return jsonPathGet(parent, path)
		}
		return nil, false
	}
	return Issue{
		Tool:     cfg.Name,
		File:     asString(get(cfg.Fields.File)),
		Line:     asInt(get(cfg.Fields.Line)),
		Rule:     asString(get(cfg.Fields.Rule)),
		Message:  asString(get(cfg.Fields.Message)),
		Severity: mapSeverity(asString(get(cfg.Fields.Severity)), cfg.SeverityMap),
	}
}

// sarifLog is the subset of SARIF one issue needs: rule, level, message, and
// the first physical location's file and start line.
type sarifLog struct {
	Runs []struct {
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
	} `json:"runs"`
}

func parseSARIF(stdout string, cfg ToolConfig) ([]Issue, error) {
	var log sarifLog
	if err := decodeOneJSON(stdout, &log); err != nil {
		return nil, fmt.Errorf("%s output was not SARIF: %w: %s", cfg.Name, err, firstLine(stdout))
	}
	var out []Issue
	for _, run := range log.Runs {
		for _, r := range run.Results {
			file, line := "", 0
			if len(r.Locations) > 0 {
				file = r.Locations[0].PhysicalLocation.ArtifactLocation.URI
				line = r.Locations[0].PhysicalLocation.Region.StartLine
			}
			out = append(out, Issue{
				Tool: cfg.Name, File: file, Line: line, Rule: r.RuleID,
				Message: r.Message.Text, Severity: mapSeverity(r.Level, cfg.SeverityMap),
			})
		}
	}
	return out, nil
}

// spectralItem is one entry of Spectral's JSON output, which vacuum's
// spectral-report command produces too. code is the rule, source the file,
// severity a number (0 error, 1 warn, 2 info, 3 hint), and range.start.line
// is 0-indexed — set lineOffset: 1 in config to render 1-indexed lines.
type spectralItem struct {
	Code     any    `json:"code"`
	Message  string `json:"message"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Range    struct {
		Start struct {
			Line int `json:"line"`
		} `json:"start"`
	} `json:"range"`
}

func parseSpectral(stdout string, cfg ToolConfig) ([]Issue, error) {
	var items []spectralItem
	if err := decodeOneJSON(stdout, &items); err != nil {
		return nil, fmt.Errorf("%s output was not Spectral JSON: %w: %s", cfg.Name, err, firstLine(stdout))
	}
	out := make([]Issue, 0, len(items))
	for _, it := range items {
		out = append(out, Issue{
			Tool: cfg.Name, File: it.Source, Line: it.Range.Start.Line,
			Rule: asString(it.Code, true), Message: it.Message,
			Severity: mapSeverity(strconv.Itoa(it.Severity), cfg.SeverityMap),
		})
	}
	return out, nil
}

// mapSeverity translates a tool's own severity spelling to Redline's. An
// explicit config entry wins; otherwise the common spellings — error/err,
// warn/warning, the Spectral numbers 0 and 1 — are recognized, and anything
// else is info, the safe floor for a signal Redline does not gate on anyway.
func mapSeverity(raw string, m map[string]string) string {
	if mapped, ok := m[raw]; ok {
		return mapped
	}
	switch strings.ToLower(raw) {
	case "error", "err", "0", "fatal", "critical", "high":
		return "error"
	case "warning", "warn", "1", "medium":
		return "warning"
	default:
		return "info"
	}
}
