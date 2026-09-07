package migrations

import "testing"

func TestScanFindsAddNotNullWithNoDefault(t *testing.T) {
	got := scanNotNull(`ALTER TABLE users ADD COLUMN tenant_id uuid NOT NULL;`)
	if len(got) != 1 {
		t.Fatalf("scanNotNull = %v, want one column", got)
	}
	if got[0].Table != "users" || got[0].Column != "tenant_id" {
		t.Fatalf("table.column = %s.%s, want users.tenant_id", got[0].Table, got[0].Column)
	}
}

func TestScanIgnoresColumnsThatCanFillThemselves(t *testing.T) {
	for _, sql := range []string{
		`ALTER TABLE users ADD COLUMN a int NOT NULL DEFAULT 0;`,
		`ALTER TABLE users ADD COLUMN b int GENERATED ALWAYS AS IDENTITY NOT NULL;`,
		`ALTER TABLE users ADD COLUMN c serial NOT NULL;`,
	} {
		if got := scanNotNull(sql); len(got) != 0 {
			t.Errorf("a column that fills every existing row is satisfiable: %q gave %v", sql, got)
		}
	}
}

func TestScanIgnoresCreateTable(t *testing.T) {
	sql := `CREATE TABLE users (id uuid PRIMARY KEY, email text NOT NULL);`
	if got := scanNotNull(sql); len(got) != 0 {
		t.Fatalf("a new table has no rows to violate the constraint, got %v", got)
	}
}

func TestScanIgnoresNullableColumns(t *testing.T) {
	if got := scanNotNull(`ALTER TABLE users ADD COLUMN nickname text;`); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestScanSkipsCommentedOutSQL(t *testing.T) {
	sql := `
-- ALTER TABLE users ADD COLUMN old_id uuid NOT NULL;
/* ALTER TABLE users ADD COLUMN older_id uuid NOT NULL; */
ALTER TABLE users ADD COLUMN real_id uuid NOT NULL;`
	got := scanNotNull(sql)
	if len(got) != 1 || got[0].Column != "real_id" {
		t.Fatalf("comments must not produce findings, got %v", got)
	}
}

func TestScanHandlesSeveralColumnsAndQuoting(t *testing.T) {
	sql := `ALTER TABLE IF EXISTS ONLY "public"."users"
	  ADD COLUMN "a" text NOT NULL,
	  ADD COLUMN b text NOT NULL DEFAULT '',
	  ADD COLUMN c text;`
	got := scanNotNull(sql)
	if len(got) != 1 {
		t.Fatalf("scanNotNull = %v, want only the undefaulted column", got)
	}
	if got[0].Column != "a" {
		t.Fatalf("column = %q, want a", got[0].Column)
	}
	if got[0].Table != "public.users" {
		t.Fatalf("table = %q, want the qualified name unquoted", got[0].Table)
	}
}

func TestScanIsCaseInsensitive(t *testing.T) {
	if got := scanNotNull(`alter table users add column x int not null;`); len(got) != 1 {
		t.Fatalf("SQL keywords are case insensitive, got %v", got)
	}
}

func TestCheckReportsOnlyAddedUpMigrations(t *testing.T) {
	p := &Pane{}
	base := &Set{Files: map[string]string{
		"m/0001_init.up.sql": "sha-old",
	}}
	head := &Set{
		Files: map[string]string{
			"m/0001_init.up.sql":     "sha-old",
			"m/0002_tenant.up.sql":   "sha-new",
			"m/0002_tenant.down.sql": "sha-down",
		},
		Content: map[string]string{
			// Already merged: the world as it is, not this change's doing.
			"m/0001_init.up.sql":     `ALTER TABLE a ADD COLUMN x int NOT NULL;`,
			"m/0002_tenant.up.sql":   `ALTER TABLE users ADD COLUMN tenant_id uuid NOT NULL;`,
			"m/0002_tenant.down.sql": `ALTER TABLE users DROP COLUMN tenant_id;`,
		},
	}
	got, confirmed := p.checkNotNull(base, head, nil)
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 (only the added up migration)", len(got))
	}
	if len(confirmed) != 0 {
		t.Fatal("a finding and a confirmation of the same rule cannot both hold")
	}
	f := got[0]
	if f.File != "m/0002_tenant.up.sql" {
		t.Fatalf("file = %q", f.File)
	}
	if f.Severity != "warning" {
		t.Fatalf("severity = %q: Redline cannot see the data, so this is conditional", f.Severity)
	}
	if f.Anchor == nil || f.Anchor.ID != "users.tenant_id" {
		t.Fatalf("anchor = %+v, want table.column so the review layer can correlate it", f.Anchor)
	}
}

func TestCheckConfirmsWhenNewMigrationsAreClean(t *testing.T) {
	p := &Pane{}
	base := &Set{Files: map[string]string{}}
	head := &Set{
		Files:   map[string]string{"m/0002_x.up.sql": "sha"},
		Content: map[string]string{"m/0002_x.up.sql": `ALTER TABLE users ADD COLUMN a text;`},
	}
	got, confirmed := p.checkNotNull(base, head, nil)
	if len(got) != 0 {
		t.Fatalf("findings = %v", got)
	}
	if len(confirmed) != 1 {
		t.Fatal("a check that ran and came back clean is a question the reviewer no longer has to ask")
	}
}

func TestCheckSaysNothingWhenNoMigrationWasAdded(t *testing.T) {
	p := &Pane{}
	base := &Set{Files: map[string]string{"m/0001_a.up.sql": "sha"}}
	head := &Set{Files: map[string]string{"m/0001_a.up.sql": "sha"}, Content: map[string]string{}}
	got, confirmed := p.checkNotNull(base, head, nil)
	if len(got) != 0 || len(confirmed) != 0 {
		t.Fatal("naming an absence the reader already knows about is noise")
	}
}
