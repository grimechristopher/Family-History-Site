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

	var userID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO core.users (email, display_name)
		VALUES ('dad@example.com', 'Dad') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var subjectID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO family.subjects (slug, kind, display_name)
		VALUES ('dad', 'individual', 'Peter John Hale') RETURNING id`).Scan(&subjectID)
	if err != nil {
		t.Fatalf("insert subject: %v", err)
	}
	var questionID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO family.questions (subject_id, asked_of_user_id, body, source)
		VALUES ($1, $2, 'What were his favorite meals?', 'import') RETURNING id`,
		subjectID, userID).Scan(&questionID)
	if err != nil {
		t.Fatalf("insert question: %v", err)
	}

	insertAnswer := func() error {
		_, err := pool.Exec(ctx, `
			INSERT INTO family.entries (question_id, author_user_id, body, is_draft)
			VALUES ($1, $2, 'Meatloaf.', false)`, questionID, userID)
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
			INSERT INTO family.entries (author_user_id, title, body, is_draft)
			VALUES ($1, 'A memory', 'Something I remembered.', false)`, userID)
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

	var userID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO core.users (email, display_name)
		VALUES ('mom@example.com', 'Mom') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO family.entries (author_user_id, body) VALUES ($1, 'No flag given.')`, userID)
	if err == nil {
		t.Error("inserting an entry without is_draft must fail")
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
