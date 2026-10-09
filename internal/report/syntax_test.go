package report

import (
	"html/template"
	"regexp"
	"strings"
	"testing"
)

var tags = regexp.MustCompile(`<[^>]+>`)

// The colouring must never change the text: a line comment copies the line's
// text content, and the agent has to get the source back exactly.
func TestHighlightKeepsTheSourceText(t *testing.T) {
	lines := []string{
		`func (c *Client) Do(ctx context.Context) error { return fmt.Errorf("x<%d>", 1) } // why`,
		`s := "unterminated`,
		`x := 'a' + "\"quoted\"" + ` + "`raw`",
		`a & b < c > d`,
	}
	for _, line := range lines {
		got, _ := goSyntax.highlight(line, plain)
		if text := template.HTMLEscapeString(line); tags.ReplaceAllString(got, "") != text {
			t.Errorf("text changed:\n got %q\nwant %q", tags.ReplaceAllString(got, ""), text)
		}
	}
}

func TestHighlightColoursGoTokens(t *testing.T) {
	got, st := goSyntax.highlight(`func run() error { return fmt.Errorf("bad %d", 42) } // note`, plain)
	for _, want := range []string{
		`<span class="sx-k">func</span>`,
		`<span class="sx-f">run</span>`,
		`<span class="sx-k">return</span>`,
		`<span class="sx-f">Errorf</span>`,
		`<span class="sx-s">&#34;bad %d&#34;</span>`,
		`<span class="sx-n">42</span>`,
		`<span class="sx-c">// note</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if st != plain {
		t.Error("a line with no open block comment must end in plain state")
	}
}

// A block comment opened on one line colours the lines after it until it
// closes, and only on its own side of the diff.
func TestHighlightCarriesBlockCommentsPerSide(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n" +
		"-/* old comment\n" +
		"+x := 1\n" +
		"-still old */\n" +
		"+y := 2\n"
	got := highlightDiffFor("a.go", diff, nil)
	if !strings.Contains(got, `-<span class="sx-c">still old */</span>`) {
		t.Errorf("the deleted line inside the comment must stay a comment:\n%s", got)
	}
	if !strings.Contains(got, `+y := <span class="sx-n">2</span>`) {
		t.Errorf("the added line must not inherit the old side's comment:\n%s", got)
	}
}

func TestHighlightLeavesUnknownFilesAlone(t *testing.T) {
	got := highlightDiffFor("README.md", "@@ -1 +1 @@\n+# for the `x` func()\n", nil)
	if strings.Contains(got, "sx-") {
		t.Errorf("a file with no known language must not be coloured:\n%s", got)
	}
}

func TestHighlightYAMLKeysAndHashComments(t *testing.T) {
	got, _ := yamlSyntax.highlight(`run-name: "build #1" # main only`, plain)
	for _, want := range []string{
		`<span class="sx-p">run-name</span>`,
		`<span class="sx-s">&#34;build #1&#34;</span>`,
		`<span class="sx-c"># main only</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	sh, _ := shSyntax.highlight(`echo $# ${#arr}`, plain)
	if strings.Contains(sh, "sx-c") {
		t.Errorf("$# is not a comment in shell:\n%s", sh)
	}
}
