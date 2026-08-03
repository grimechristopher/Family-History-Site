package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// userColumns is identity only. Role, person and the queue settings are not
// properties of a person -- somebody is an admin in one family and a contributor in
// another -- so they live on core.family_members and are merged onto the User once
// the request's family is known. Before that they are zero.
const userColumns = `id, email, supabase_user_id, display_name`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.SupabaseUserID, &u.DisplayName)
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
	return scanUser(s.q(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM core.users WHERE lower(email) = lower($1)`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.q(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM core.users WHERE id = $1`, id))
}

func (s *Store) UserByDisplayName(ctx context.Context, name string) (*User, error) {
	return scanUser(s.q(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM core.users WHERE display_name = $1`, name))
}

// Contributors are the people this family actually asks something of.
//
// Having at least one question is the test, not the role. A spouse who belongs to
// the other side of the family is a member here so she can read her husband's
// answers, but nothing is asked of her here -- offering her under "whose
// questions" leads to an empty page.
// familySlug narrows to the people who answer in one line. Frank belongs only to
// the Lucero line, so choosing the Grime line should not go on offering him.
// Contributors lists everybody in this line who can be asked a question and
// already has one waiting -- the "Whose questions" filter on the browsable
// questions page. Offering somebody with nothing to filter by is a dead end, so
// this requires history; Askable below does not.
func (s *Store) Contributors(ctx context.Context, familySlug string) ([]*User, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT DISTINCT `+prefixed(userColumns, "u.")+`
		  FROM core.users u
		  JOIN core.family_members m ON m.user_id = u.id
		  JOIN core.families f ON f.id = m.family_id
		 WHERE m.family_id = ANY($1) AND m.removed_at IS NULL AND m.askable
		   AND ($2 = '' OR f.slug = $2)
		   AND EXISTS (SELECT 1 FROM family.questions q
		                JOIN family.question_askees qa ON qa.question_id = q.id
		               WHERE qa.user_id = u.id
		                 AND q.family_id = m.family_id
		                 AND q.archived_at IS NULL)
		 GROUP BY u.id, u.email, u.supabase_user_id, u.display_name
		 ORDER BY u.display_name`, FamilyIDsFrom(ctx), familySlug)
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

// Askable lists everybody in this line who can be asked a question, with no
// history required. The checkbox that offers who a new question goes to, and the
// handler that validates it, both need this rather than Contributors: otherwise
// nobody added after the initial import could ever be asked their first thing,
// since the very query that decides who's offered would require a question that
// doesn't exist yet.
func (s *Store) Askable(ctx context.Context, familySlug string) ([]*User, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT DISTINCT `+prefixed(userColumns, "u.")+`
		  FROM core.users u
		  JOIN core.family_members m ON m.user_id = u.id
		  JOIN core.families f ON f.id = m.family_id
		 WHERE m.family_id = ANY($1) AND m.removed_at IS NULL AND m.askable
		   AND ($2 = '' OR f.slug = $2)
		 ORDER BY u.display_name`, FamilyIDsFrom(ctx), familySlug)
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
// UpsertUser seeds or updates an identity. It says nothing about families: use
// AddMember for that, so the role is recorded where it is true.
func UpsertUser(ctx context.Context, db DBTX, email, displayName string) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO core.users (email, display_name)
		VALUES (lower($1), $2)
		ON CONFLICT (email) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id`, email, displayName).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert user %s: %w", email, err)
	}
	return id, nil
}

// SetUserEmail moves a person's sign-in address, keeping the row they already
// have -- and with it every question asked of them. Upserting by email would
// insert a second person instead.
//
// The stored Supabase identity is cleared at the same time. It belongs to the old
// address, and leaving it would have the next sign-in arrive with a different
// subject than the one on file.
func (s *Store) SetUserEmail(ctx context.Context, userID int64, email string) error {
	_, err := s.q(ctx).Exec(ctx, `
		UPDATE core.users
		   SET email = lower($2), supabase_user_id = NULL
		 WHERE id = $1`, userID, email)
	if err != nil {
		return fmt.Errorf("set email for user %d: %w", userID, err)
	}
	return nil
}

func LinkUserToPerson(ctx context.Context, db DBTX, userID, personID int64) error {
	_, err := db.Exec(ctx,
		`UPDATE core.family_members SET person_id = $3
		  WHERE family_id = $1 AND user_id = $2`, FamilyFrom(ctx), userID, personID)
	return err
}

// ErrIdentityClaimed means another allowlist row already holds this Supabase
// identity. That is a misconfiguration rather than a user error, so it is
// reported distinctly instead of surfacing as a database failure mid-login.
var ErrIdentityClaimed = errors.New("supabase identity already claimed by another user")

// BackfillSupabaseUserID records the Supabase identity on first login, since rows
// are seeded by email before anyone has ever logged in.
func (s *Store) BackfillSupabaseUserID(ctx context.Context, userID int64, supabaseID string) error {
	_, err := s.q(ctx).Exec(ctx,
		`UPDATE core.users SET supabase_user_id = $2 WHERE id = $1`, userID, supabaseID)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrIdentityClaimed
	}
	return err
}

func (s *Store) SetQueueMode(ctx context.Context, userID int64, mode string, focusSubjectID *int64) error {
	_, err := s.q(ctx).Exec(ctx,
		`UPDATE core.family_members SET queue_mode = $3, queue_focus_subject_id = $4
		  WHERE family_id = ANY($1) AND user_id = $2`,
		FamilyIDsFrom(ctx), userID, mode, focusSubjectID)
	return err
}
