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
	if _, err := pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS family CASCADE"); err != nil {
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

	for _, table := range []string{
		"people", "subjects", "subject_members", "users", "questions",
		"question_deferrals", "entries", "replies", "attachments", "sessions",
	} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema='family' AND table_name=$1)`, table).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table family.%s was not created", table)
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
		INSERT INTO family.users (email, display_name, role)
		VALUES ('dad@example.com', 'Dad', 'contributor') RETURNING id`).Scan(&userID)
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
		INSERT INTO family.users (email, display_name, role)
		VALUES ('mom@example.com', 'Mom', 'contributor') RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO family.entries (author_user_id, body) VALUES ($1, 'No flag given.')`, userID)
	if err == nil {
		t.Error("inserting an entry without is_draft must fail")
	}
}
