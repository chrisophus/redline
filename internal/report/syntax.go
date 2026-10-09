package report

import (
	"html/template"
	"path"
	"strings"
)

// syntax describes one language closely enough to colour a diff line by line:
// keywords, literal constants, comments and strings. It is not a parser. A
// line that starts inside a construct a hunk header cut through (a block
// comment opened above the hunk) colours as code, which is the cost of not
// shipping a highlighting library in a page that must work offline.
type syntax struct {
	keywords map[string]bool
	literals map[string]bool
	// fold matches keywords without regard to case, for SQL.
	fold bool
	// lineComment lists the prefixes that comment out the rest of a line.
	lineComment []string
	// spans are constructs that can run across lines: block comments and
	// multi-line strings. Longer openers go first so """ wins over ".
	spans []span
	// quotes are the single-line string delimiters.
	quotes string
	// dashIdent lets an identifier contain '-', as YAML keys and CSS
	// properties do.
	dashIdent bool
	// keys colours a name or string followed by ':' as a key, for YAML and JSON.
	keys bool
}

type span struct {
	open, close, class string
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

var (
	cBlock = span{"/*", "*/", "sx-c"}

	goSyntax = &syntax{
		keywords: words(`break case chan const continue default defer else fallthrough for func go goto
			if import interface map package range return select struct switch type var`),
		literals:    words("true false nil iota"),
		lineComment: []string{"//"},
		spans:       []span{cBlock, {"`", "`", "sx-s"}},
		quotes:      `"'`,
	}
	jsSyntax = &syntax{
		keywords: words(`abstract as async await break case catch class const continue debugger declare
			default delete do else enum export extends finally for from function get if implements import
			in instanceof interface let namespace new of private protected public readonly return set
			static super switch this throw try type typeof var void while with yield`),
		literals:    words("true false null undefined NaN Infinity"),
		lineComment: []string{"//"},
		spans:       []span{cBlock, {"`", "`", "sx-s"}},
		quotes:      `"'`,
	}
	pySyntax = &syntax{
		keywords: words(`and as assert async await break case class continue def del elif else except
			finally for from global if import in is lambda match nonlocal not or pass raise return try
			while with yield`),
		literals:    words("True False None self"),
		lineComment: []string{"#"},
		spans:       []span{{`"""`, `"""`, "sx-s"}, {"'''", "'''", "sx-s"}},
		quotes:      `"'`,
	}
	rustSyntax = &syntax{
		keywords: words(`as async await break const continue crate dyn else enum extern fn for if impl in
			let loop match mod move mut pub ref return self Self static struct super trait type unsafe use
			where while`),
		literals:    words("true false None Some Ok Err"),
		lineComment: []string{"//"},
		spans:       []span{cBlock},
		// No single quote: Rust uses it for lifetimes ('a), and reading those
		// as strings would colour the rest of the line.
		quotes: `"`,
	}
	cSyntax = &syntax{
		keywords: words(`abstract auto break case catch char class const continue default delete do double
			else enum extends extern final finally float for fun func goto guard if implements import int
			interface let long namespace new override package private protected public return short
			signed sizeof static struct super switch template this throw try typedef typename union
			unsigned using val var virtual void volatile when while`),
		literals:    words("true false null nullptr NULL nil"),
		lineComment: []string{"//"},
		spans:       []span{cBlock},
		quotes:      `"'`,
	}
	rubySyntax = &syntax{
		keywords: words(`alias and begin break case class def do else elsif end ensure for if in module
			next not or redo rescue retry return self super then unless until when while yield`),
		literals:    words("true false nil"),
		lineComment: []string{"#"},
		quotes:      `"'`,
	}
	shSyntax = &syntax{
		keywords: words(`case do done elif else esac exit export fi for function if in local readonly
			return select then until while`),
		literals:    words("true false"),
		lineComment: []string{"#"},
		quotes:      `"'`,
	}
	sqlSyntax = &syntax{
		keywords: words(`add alter and as asc begin between by cascade case check column commit constraint
			create default delete desc distinct drop else end exists foreign from group having if in index
			inner insert into is join key left like limit not null offset on or order outer primary
			references returning right rollback select set table then transaction union unique update
			using values view when where with`),
		literals:    words("true false"),
		fold:        true,
		lineComment: []string{"--"},
		spans:       []span{cBlock},
		quotes:      `"'`,
	}
	yamlSyntax = &syntax{
		literals:    words("true false null yes no ~"),
		lineComment: []string{"#"},
		quotes:      `"'`,
		dashIdent:   true,
		keys:        true,
	}
	jsonSyntax = &syntax{
		literals: words("true false null"),
		quotes:   `"`,
		keys:     true,
	}
	cssSyntax = &syntax{
		literals:  words("important"),
		spans:     []span{cBlock},
		quotes:    `"'`,
		dashIdent: true,
	}
	// hashSyntax is for config-ish files whose only reliable structure is
	// '#' comments and quoted strings.
	hashSyntax = &syntax{
		literals:    words("true false"),
		lineComment: []string{"#"},
		quotes:      `"'`,
	}
)

var syntaxByExt = map[string]*syntax{
	".go": goSyntax,
	".js": jsSyntax, ".mjs": jsSyntax, ".cjs": jsSyntax, ".jsx": jsSyntax,
	".ts": jsSyntax, ".tsx": jsSyntax, ".mts": jsSyntax, ".cts": jsSyntax,
	".py": pySyntax, ".pyi": pySyntax,
	".rs": rustSyntax,
	".c":  cSyntax, ".h": cSyntax, ".cc": cSyntax, ".cpp": cSyntax, ".hpp": cSyntax,
	".java": cSyntax, ".kt": cSyntax, ".kts": cSyntax, ".swift": cSyntax, ".cs": cSyntax,
	".scala": cSyntax, ".dart": cSyntax,
	".rb": rubySyntax,
	".sh": shSyntax, ".bash": shSyntax, ".zsh": shSyntax,
	".sql":  sqlSyntax,
	".yaml": yamlSyntax, ".yml": yamlSyntax,
	".json": jsonSyntax,
	".css":  cssSyntax, ".scss": cssSyntax, ".less": cssSyntax,
	".toml": hashSyntax, ".tf": hashSyntax,
}

var syntaxByName = map[string]*syntax{
	"Makefile": hashSyntax, "Dockerfile": hashSyntax, "Containerfile": hashSyntax,
	"Gemfile": rubySyntax, "Rakefile": rubySyntax,
}

// syntaxFor picks a language from a file path. Nil means no colouring, which
// is the right answer for prose such as Markdown.
func syntaxFor(p string) *syntax {
	base := path.Base(p)
	if s, ok := syntaxByName[base]; ok {
		return s
	}
	return syntaxByExt[strings.ToLower(path.Ext(base))]
}

// lineState is where a line ended: -1 in plain code, otherwise the index into
// syntax.spans of the construct still open.
type lineState int

const plain lineState = -1

// highlight returns code as escaped HTML with tokens wrapped in spans, and the
// state the next line of the same file starts in. The text content of the
// result is exactly code, so copying a line or reading it back from the DOM
// gives the source unchanged.
func (sx *syntax) highlight(code string, st lineState) (string, lineState) {
	var b strings.Builder
	text := 0 // start of the pending run of uncoloured text
	flush := func(to int) {
		if to > text {
			b.WriteString(template.HTMLEscapeString(code[text:to]))
		}
	}
	emit := func(from, to int, class string) {
		flush(from)
		b.WriteString(`<span class="`)
		b.WriteString(class)
		b.WriteString(`">`)
		b.WriteString(template.HTMLEscapeString(code[from:to]))
		b.WriteString(`</span>`)
		text = to
	}

	i := 0
	if st != plain {
		sp := sx.spans[st]
		end := strings.Index(code, sp.close)
		if end < 0 {
			emit(0, len(code), sp.class)
			return b.String(), st
		}
		i = end + len(sp.close)
		emit(0, i, sp.class)
	}

scan:
	for i < len(code) {
		rest := code[i:]
		for _, p := range sx.lineComment {
			// A '#' only starts a comment after whitespace: YAML requires it,
			// and in shell it is part of $# and ${#x}.
			if strings.HasPrefix(rest, p) && (p != "#" || i == 0 || code[i-1] == ' ' || code[i-1] == '\t') {
				emit(i, len(code), "sx-c")
				break scan
			}
		}
		for k, sp := range sx.spans {
			if !strings.HasPrefix(rest, sp.open) {
				continue
			}
			end := strings.Index(code[i+len(sp.open):], sp.close)
			if end < 0 {
				emit(i, len(code), sp.class)
				return b.String(), lineState(k)
			}
			j := i + len(sp.open) + end + len(sp.close)
			emit(i, j, sp.class)
			i = j
			continue scan
		}
		c := code[i]
		switch {
		case strings.IndexByte(sx.quotes, c) >= 0:
			j := i + 1
			for j < len(code) && code[j] != c {
				if code[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(code) {
				j++
			}
			if j > len(code) {
				j = len(code)
			}
			class := "sx-s"
			if sx.keys && nextNonSpace(code, j) == ':' {
				class = "sx-p"
			}
			emit(i, j, class)
			i = j
		case isDigit(c) && (i == 0 || !isIdent(code[i-1], sx.dashIdent)):
			j := i + 1
			for j < len(code) && (isIdent(code[j], false) || code[j] == '.') {
				j++
			}
			emit(i, j, "sx-n")
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(code) && isIdent(code[j], sx.dashIdent) {
				j++
			}
			w := code[i:j]
			key := w
			if sx.fold {
				key = strings.ToLower(w)
			}
			next := nextNonSpace(code, j)
			switch {
			case sx.keys && next == ':':
				emit(i, j, "sx-p")
			case sx.keywords[key]:
				emit(i, j, "sx-k")
			case sx.literals[key]:
				emit(i, j, "sx-l")
			case next == '(' && sx.keywords != nil:
				emit(i, j, "sx-f")
			}
			i = j
		default:
			i++
		}
	}
	flush(len(code))
	return b.String(), plain
}

func nextNonSpace(s string, i int) byte {
	for ; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' {
			return s[i]
		}
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdent(c byte, dash bool) bool {
	return isIdentStart(c) || isDigit(c) || (dash && c == '-')
}
