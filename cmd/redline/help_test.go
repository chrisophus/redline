package main

import (
	"errors"
	"flag"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var helpFlagRE = regexp.MustCompile(`(?m)^\s{2}(--[a-z][a-z-]*)(?:,\s*(--[a-z][a-z-]*))*(?:,\s*(--[a-z][a-z-]*))?`)

// listedFlags is every flag a help text lists as an entry: the ones that open
// an indented line, including a comma-separated group such as post's
// "--branch, --commit, --range". A flag mentioned inside a description is not
// an entry and is not collected.
func listedFlags(help string) []string {
	var out []string
	for _, m := range helpFlagRE.FindAllStringSubmatch(help, -1) {
		line := m[0]
		for _, f := range regexp.MustCompile(`--[a-z][a-z-]*`).FindAllString(line, -1) {
			out = append(out, strings.TrimPrefix(f, "--"))
		}
	}
	return out
}

// Each command's help lists exactly the flags that command parses, less the
// global ones `redline help` lists once. A flag added to a command without a
// help line, or a help line left behind for a flag that moved, fails here.
func TestCommandHelpListsItsFlags(t *testing.T) {
	for _, c := range commands {
		registered := registeredFlags(c)
		listed := listedFlags(c.help)
		for _, name := range registered {
			if slices.Contains(globalFlags, name) {
				continue
			}
			if !slices.Contains(listed, name) {
				t.Errorf("%s registers --%s but its help does not list it", c.name, name)
			}
		}
		for _, name := range listed {
			if !slices.Contains(registered, name) {
				t.Errorf("%s help lists --%s but the command does not accept it", c.name, name)
			}
		}
	}
}

func TestTopLevelHelpListsCommandsAndGlobalFlagsOnly(t *testing.T) {
	top := usage()
	for _, c := range commands {
		if !strings.Contains(top, "  "+c.name+" ") {
			t.Errorf("redline help does not list %s", c.name)
		}
	}
	for _, name := range globalFlags {
		if !slices.Contains(listedFlags(top), name) {
			t.Errorf("redline help does not list the global flag --%s", name)
		}
	}
	for _, name := range listedFlags(top) {
		if !slices.Contains(globalFlags, name) && name != "help" {
			t.Errorf("redline help lists --%s, which is not a global flag", name)
		}
	}
}

// A flag belonging to another command is refused, and the error names where
// the right flags are listed.
func TestForeignFlagIsRefused(t *testing.T) {
	err := runMain([]string{"review", "--pr", "7"})
	if err == nil {
		t.Fatal("review accepted --pr, which only run and post read")
	}
	if !strings.Contains(err.Error(), "redline help review") {
		t.Errorf("error does not point at the command's help: %v", err)
	}
}

func TestHelpForms(t *testing.T) {
	for _, args := range [][]string{
		{"help"}, {"-h"}, {"--help"}, {},
		{"help", "review"}, {"review", "-h"}, {"review", "--help"}, {"gc", "-h"},
	} {
		if err := runMain(args); err != nil {
			t.Errorf("runMain(%q) = %v, want help and no error", args, err)
		}
	}
	if err := runMain([]string{"help", "nope"}); err == nil {
		t.Error("help for an unknown command returned no error")
	}
	var o opts
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reviewFlags(fs, &o)
	if err := fs.Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h parsed as %v, want flag.ErrHelp", err)
	}
}
