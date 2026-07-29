package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID                  int64
	Email               string
	SupabaseUserID      *string
	DisplayName         string
	PersonID            *int64
	Role                string
	QueueMode           string
	QueueSeed           int64
	QueueFocusSubjectID *int64
	DigestEnabled       bool
}

const (
	RoleAdmin       = "admin"
	RoleContributor = "contributor"

	QueueInOrder    = "in_order"
	QueueShuffle    = "shuffle"
	QueueOneSubject = "one_subject"
)

const userColumns = `id, email, supabase_user_id, display_name, person_id, role,
	queue_mode, queue_seed, queue_focus_subject_id, digest_enabled`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.SupabaseUserID, &u.DisplayName, &u.PersonID,
		&u.Role, &u.QueueMode, &u.QueueSeed, &u.QueueFocusSubjectID, &u.DigestEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UserByEmail is the allowlist check. A verified Supabase login with no row here
// gets no access, which is what keeps portfolio signups out of the family history.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM family.users WHERE lower(email) = lower($1)`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM family.users WHERE id = $1`, id))
}

func (s *Store) UserByDisplayName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.Pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM family.users WHERE display_name = $1`, name))
}

// Contributors are the people questions get asked of, in display-name order.
func (s *Store) Contributors(ctx context.Context) ([]*User, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT `+userColumns+` FROM family.users WHERE role = 'contributor' ORDER BY display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertUser seeds or updates an allowlist entry. Called by the import command,
// never from a request handler.
func UpsertUser(ctx context.Context, db DBTX, email, displayName, role string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO family.users (email, display_name, role, queue_seed)
		VALUES (lower($1), $2, $3, floor(random() * 1e9)::bigint)
		ON CONFLICT (email) DO UPDATE
		  SET display_name = EXCLUDED.display_name, role = EXCLUDED.role
		RETURNING id`, email, displayName, role).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert user %s: %w", email, err)
	}
	return id, nil
}

func LinkUserToPerson(ctx context.Context, db DBTX, userID, personID int64) error {
	_, err := db.Exec(ctx,
		`UPDATE family.users SET person_id = $2 WHERE id = $1`, userID, personID)
	return err
}

// BackfillSupabaseUserID records the Supabase identity on first login, since rows
// are seeded by email before anyone has ever logged in.
func (s *Store) BackfillSupabaseUserID(ctx context.Context, userID int64, supabaseID string) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE family.users SET supabase_user_id = $2 WHERE id = $1`, userID, supabaseID)
	return err
}

func (s *Store) SetQueueMode(ctx context.Context, userID int64, mode string, focusSubjectID *int64) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE family.users SET queue_mode = $2, queue_focus_subject_id = $3 WHERE id = $1`,
		userID, mode, focusSubjectID)
	return err
}
