package migrate

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip(`TEST_DATABASE_URL not set; run: eval "$(scripts/testdb.sh start)"`)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(),
		"DROP SCHEMA IF EXISTS family CASCADE; DROP SCHEMA IF EXISTS core CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	return pool
}

func TestRunAppliesMigrationsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM family.schema_migrations").Scan(&n); err != nil {
			t.Fatalf("count migrations: %v", err)
		}
		return n
	}

	if err := Run(ctx, pool); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	applied := count()
	if applied == 0 {
		t.Fatal("no migrations were recorded")
	}

	if err := Run(ctx, pool); err != nil {
		t.Fatalf("second Run should be a no-op: %v", err)
	}
	// The assertion is that re-running changes nothing, not that any particular
	// number of migration files exists.
	if again := count(); again != applied {
		t.Errorf("recorded migrations went %d -> %d; re-running must not duplicate", applied, again)
	}

	// Family data stays in family; identity moved to core in 0003.
	for schema, tables := range map[string][]string{
		"family": {"people", "subjects", "subject_members", "questions",
			"question_deferrals", "entries", "replies", "attachments"},
		"core": {"users", "sessions", "families", "family_members", "invites"},
	} {
		for _, table := range tables {
			var exists bool
			err := pool.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM information_schema.tables
				                WHERE table_schema=$1 AND table_name=$2)`, schema, table).Scan(&exists)
			if err != nil {
				t.Fatalf("check %s.%s: %v", schema, table, err)
			}
			if !exists {
				t.Errorf("table %s.%s was not created", schema, table)
			}
		}
	}
}

// One answer per person per question, but unlimited stories per person. This is
// load-bearing for the whole answer model, so it is asserted at the schema level.
func TestEntriesUniqueConstraintAllowsManyStories(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	famID, userID := seedMember(t, pool, "dadfam", "dad@example.com", "Dad")

	var subjectID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO family.subjects (family_id, slug, kind, display_name)
		VALUES ($1, 'dad', 'individual', 'Peter John Hale') RETURNING id`, famID).Scan(&subjectID)
	if err != nil {
		t.Fatalf("insert subject: %v", err)
	}
	var questionID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO family.questions (family_id, subject_id, asked_of_user_id, body, source)
		VALUES ($1, $2, $3, 'What were his favorite meals?', 'import') RETURNING id`,
		famID, subjectID, userID).Scan(&questionID)
	if err != nil {
		t.Fatalf("insert question: %v", err)
	}

	insertAnswer := func() error {
		_, err := pool.Exec(ctx, `
			INSERT INTO family.entries (family_id, question_id, author_user_id, body, is_draft)
			VALUES ($1, $2, $3, 'Meatloaf.', false)`, famID, questionID, userID)
		return err
	}
	if err := insertAnswer(); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	if err := insertAnswer(); err == nil {
		t.Error("a second answer to the same question by the same author must be rejected")
	}

	// Stories carry a NULL question_id, and NULLs are distinct in a unique index.
	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO family.entries (family_id, author_user_id, title, body, is_draft)
			VALUES ($1, $2, 'A memory', 'Something I remembered.', false)`, famID, userID)
		if err != nil {
			t.Fatalf("story %d should be allowed: %v", i, err)
		}
	}
}

// is_draft must have no default, so neither call site can forget to state it.
func TestEntriesRequiresExplicitDraftFlag(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("Run: %v", err)
	}

	famID, userID := seedMember(t, pool, "momfam", "mom@example.com", "Mom")

	// Everything except is_draft is supplied, so the only reason this can fail is
	// the missing flag. Leaving family_id out as well would make the test pass
	// whether or not is_draft has a default.
	_, err := pool.Exec(ctx, `
		INSERT INTO family.entries (family_id, author_user_id, body)
		VALUES ($1, $2, 'No flag given.')`, famID, userID)
	if err == nil {
		t.Error("inserting an entry without is_draft must fail")
	}

	// And the same row with the flag succeeds, which is what proves the failure
	// above was about is_draft and nothing else.
	if _, err := pool.Exec(ctx, `
		INSERT INTO family.entries (family_id, author_user_id, body, is_draft)
		VALUES ($1, $2, 'Flag given.', false)`, famID, userID); err != nil {
		t.Fatalf("the same entry with is_draft should insert: %v", err)
	}
}

// Identity is shared across families, so it moves into its own schema. The
// per-family columns move onto membership: somebody is an admin in one family and
// a contributor in another, and their place in the card stack differs per family.
func TestCoreSchemaHoldsIdentity(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{"users", "sessions", "families", "family_members", "invites"} {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema = 'core' AND table_name = $1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check core.%s: %v", table, err)
		}
		if !exists {
			t.Errorf("core.%s does not exist", table)
		}
	}

	// Anything true of a person only within one family must have left users.
	for _, column := range []string{"role", "person_id", "queue_mode", "queue_seed", "digest_enabled"} {
		var onUsers bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_schema='core' AND table_name='users' AND column_name=$1)`,
			column).Scan(&onUsers)
		if err != nil {
			t.Fatalf("check users.%s: %v", column, err)
		}
		if onUsers {
			t.Errorf("core.users still has %s; it belongs on family_members", column)
		}
	}
}

// A row must not be able to reference another family. Postgres refuses it rather
// than trusting every query in Go to have checked.
func TestCrossFamilyRowsAreRefused(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	famA := seedFamily(t, pool, "a", "A")
	famB := seedFamily(t, pool, "b", "B")

	var subjA int64
	err := pool.QueryRow(ctx, `
		INSERT INTO family.subjects (family_id, slug, kind, display_name, sort_order)
		VALUES ($1,'x','individual','X',1) RETURNING id`, famA).Scan(&subjA)
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO family.questions (family_id, subject_id, body, sort_order, import_key)
		VALUES ($1, $2, 'q', 1, 'k')`, famB, subjA)
	if err == nil {
		t.Fatal("a question in one family was allowed to reference another family's subject")
	}
}

// The same slug in two families is ordinary, not a conflict: two Ancestry exports
// both contain @I1@, and two families may both have a subject called her-father.
func TestSlugsAreUniquePerFamilyNotGlobally(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	if err := Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, slug := range []string{"a", "b"} {
		fam := seedFamily(t, pool, slug, slug)
		if _, err := pool.Exec(ctx, `
			INSERT INTO family.subjects (family_id, slug, kind, display_name, sort_order)
			VALUES ($1,'her-father','individual','Her father',1)`, fam); err != nil {
			t.Fatalf("the same subject slug in a second family was refused: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO family.people (family_id, gedcom_id, given_name, surname)
			VALUES ($1,'@I1@','A','B')`, fam); err != nil {
			t.Fatalf("the same gedcom id in a second family was refused: %v", err)
		}
	}
}

func seedFamily(t *testing.T, pool *pgxpool.Pool, slug, name string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO core.families (slug, display_name) VALUES ($1,$2) RETURNING id`,
		slug, name).Scan(&id)
	if err != nil {
		t.Fatalf("seed family %s: %v", slug, err)
	}
	return id
}

// seedMember creates a family, a user and the membership between them. The
// membership is not optional: entries and questions reference
// core.family_members, so a user who is not in the family cannot author anything.
func seedMember(t *testing.T, pool *pgxpool.Pool, slug, email, name string) (familyID, userID int64) {
	t.Helper()
	ctx := context.Background()
	familyID = seedFamily(t, pool, slug, slug)
	err := pool.QueryRow(ctx,
		`INSERT INTO core.users (email, display_name) VALUES ($1,$2) RETURNING id`,
		email, name).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO core.family_members (family_id, user_id) VALUES ($1,$2)`,
		familyID, userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return familyID, userID
}
