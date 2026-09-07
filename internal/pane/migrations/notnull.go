package migrations

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisophus/redline/internal/findings"
)

// This file is check 3: a migration adds a NOT NULL column to an existing
// table without a default.
//
// Checks 1 and 2 compare filenames and blobs. This one reads SQL, which is a
// different kind of claim, so it is deliberately narrow. Only ALTER TABLE ...
// ADD COLUMN is examined: a NOT NULL column on a CREATE TABLE is fine, because
// the table has no rows yet. On a table that does have rows, adding a NOT NULL
// column with no default fails outright, and on a table that might, the change
// only works if a backfill runs first. Redline cannot see the data, so this
// reports the shape and says what it depends on.
//
// It also exists to give the review layer a fact worth correlating. A column
// that is NOT NULL in the database and a non-pointer field with no default in
// the code are each unremarkable alone. Read together they say the write path
// will insert a zero value where the schema wanted a real one. No single
// producer reaches that, which is the whole argument for a second wave.

// stmtSep splits a script into statements. Naive on purpose: a semicolon
// inside a dollar-quoted function body would split wrongly, and the result is
// a statement this check does not recognise rather than a wrong finding.
var stmtSep = regexp.MustCompile(`;`)

var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// alterTable captures the table name of an ALTER TABLE statement,
	// tolerating IF EXISTS, ONLY, and a schema qualifier.
	alterTable = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([^\s(]+)`)
	// addColumn captures each ADD COLUMN clause's column name. COLUMN is
	// optional in Postgres and MySQL alike.
	addColumn = regexp.MustCompile(`(?is)\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([^\s(,]+)([^,]*)`)
	notNull   = regexp.MustCompile(`(?is)\bNOT\s+NULL\b`)
	hasParts  = regexp.MustCompile(`(?is)\b(DEFAULT|GENERATED|IDENTITY|SERIAL)\b`)
)

// NotNullColumn is one ADD COLUMN ... NOT NULL with no default.
type NotNullColumn struct {
	Table  string
	Column string
	Clause string
}

// scanNotNull returns every NOT NULL column added to an existing table by
// this SQL, in source order.
func scanNotNull(sql string) []NotNullColumn {
	sql = blockComment.ReplaceAllString(sql, " ")
	sql = lineComment.ReplaceAllString(sql, " ")

	var out []NotNullColumn
	for _, stmt := range stmtSep.Split(sql, -1) {
		m := alterTable.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		table := unquote(m[1])
		for _, c := range addColumn.FindAllStringSubmatch(stmt, -1) {
			clause := strings.TrimSpace(c[0])
			rest := c[2]
			if !notNull.MatchString(rest) {
				continue
			}
			// A default, an identity, or a serial fills the column for
			// every existing row, so the constraint is satisfiable.
			if hasParts.MatchString(rest) {
				continue
			}
			out = append(out, NotNullColumn{
				Table:  table,
				Column: unquote(c[1]),
				Clause: collapse(clause),
			})
		}
	}
	return out
}

// unquote strips identifier quoting. Every quote character is removed rather
// than only the outer pair, because a schema-qualified name arrives as
// "public"."users" and trimming the ends leaves the inner quotes behind.
func unquote(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '`', '\'':
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// checkNotNull is check 3 over the migrations this change adds. Only added
// files are read: an existing migration that already did this is the world as
// it is, and check 1 owns whether this change edited one.
func (p *Pane) checkNotNull(base, head *Set, evidence []string) ([]findings.Finding, []findings.Confirmation) {
	var paths []string
	for path := range head.Files {
		if _, existed := base.Files[path]; existed {
			continue
		}
		if _, dir, ok := parse(path); !ok || dir != "up" {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var out []findings.Finding
	var scanned int
	for _, path := range paths {
		sql, ok := head.Content[path]
		if !ok {
			continue
		}
		scanned++
		version, _, _ := parse(path)
		for _, c := range scanNotNull(sql) {
			out = append(out, findings.Finding{
				File:      path,
				Rule:      "migration-add-not-null-no-default",
				Substrate: Substrate,
				Category:  findings.CategorySchema,
				// Not an error: on an empty table this is correct, and
				// Redline cannot see the data. It is a fact with a stated
				// precondition, which is what a reviewer needs.
				Severity: findings.SeverityWarning,
				Message: fmt.Sprintf("migration %s adds NOT NULL column %s.%s with no default",
					version, c.Table, c.Column),
				Anchor:   &findings.Anchor{Kind: "table.column", ID: c.Table + "." + c.Column},
				Evidence: evidence,
				Expected: "a default, or a backfill before the constraint",
				Observed: c.Clause,
				FixCmd:   "add a DEFAULT, or split into add-nullable, backfill, then set NOT NULL",
				Context: "Every row that already exists needs a value. Without a default the statement " +
					"fails on a non-empty table, and any write path that omits the column starts failing " +
					"the moment this lands.",
			})
		}
	}
	if scanned > 0 && len(out) == 0 {
		return nil, []findings.Confirmation{{
			Substrate: Substrate,
			Rule:      "migration-add-not-null-no-default",
			Message: fmt.Sprintf("no NOT NULL column is added without a default across %d new migration(s)",
				scanned),
		}}
	}
	return out, nil
}
