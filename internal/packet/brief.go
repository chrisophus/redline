package packet

// Brief is what a cheap context-gathering pass leaves for the reviewing agent:
// which symbols the change touches elsewhere, which docs name its commands,
// which tests cover them, and which files the pass itself opened. Copilot's
// edge on PR review came from exactly this: reading outside the diff before
// judging it. The brief is that pass, recorded so the agent does not have to
// rediscover it and so the report can say what was examined beyond the diff.
type Brief struct {
	// RepoShape is one paragraph: what this repository is and how it is laid out.
	RepoShape string `json:"repoShape,omitempty"`

	// References maps identifiers the change introduces or renames to the other
	// files that mention them. Empty AlsoIn means the brief looked and found
	// none, which is different from the symbol not being checked.
	References []BriefRef `json:"references,omitempty"`

	// DocsNaming are documentation or skill passages that name a command, flag,
	// or behaviour this change adds or alters. A contradiction between the doc
	// and the code is a finding the diff alone will not surface.
	DocsNaming []BriefDoc `json:"docsNaming,omitempty"`

	// TestsCovering are tests that exercise the changed behaviour, or the
	// absence of any: a note that the subcommand dispatch omits the new command
	// is as useful as a passing table.
	TestsCovering []BriefTest `json:"testsCovering,omitempty"`

	// FilesRead is every path the brief actually opened. It feeds the coverage
	// line so a reader can see what was examined outside the diff, not only
	// which of the changed files the panes touched.
	FilesRead []string `json:"filesRead,omitempty"`
}

// BriefRef is one symbol and where else it appears.
type BriefRef struct {
	Symbol    string   `json:"symbol"`
	DefinedIn string   `json:"definedIn,omitempty"`
	AlsoIn    []string `json:"alsoIn,omitempty"`
}

// BriefDoc is one documentation hit that names something in the change.
type BriefDoc struct {
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Quote string `json:"quote,omitempty"`
	About string `json:"about,omitempty"`
}

// BriefTest is one test (or test gap) that covers the change.
type BriefTest struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Note string `json:"note,omitempty"`
}
