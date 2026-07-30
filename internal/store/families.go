package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Family is one household's worth of people, subjects and questions. Families
// never see each other: every family table carries family_id, and row-level
// security filters on it.
type Family struct {
	ID          int64
	Slug        string
	DisplayName string
}

// Membership is everything true of a person within one family. It is separate
// from User because none of it generalises: somebody is an admin in their own
// family and a contributor in their in-laws', their place in the card stack
// differs in each, and so does which person in the tree they are.
type Membership struct {
	FamilyID            int64
	UserID              int64
	Role                string
	PersonID            *int64
	QueueMode           string
	QueueSeed           int64
	QueueFocusSubjectID *int64
	DigestEnabled       bool
}

// FamilyBySlug looks a family up regardless of who is asking. Whether the request
// may proceed is a separate question, answered by MembershipOf.
func (s *Store) FamilyBySlug(ctx context.Context, slug string) (*Family, error) {
	var f Family
	err := s.q(ctx).QueryRow(ctx,
		`SELECT id, slug, display_name FROM core.families WHERE slug = $1`, slug).
		Scan(&f.ID, &f.Slug, &f.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("family by slug %q: %w", slug, err)
	}
	return &f, nil
}

// MembershipOf returns what somebody is within a family, or ErrNotFound when they
// are not in it at all.
func (s *Store) MembershipOf(ctx context.Context, familyID, userID int64) (*Membership, error) {
	var m Membership
	err := s.q(ctx).QueryRow(ctx, `
		SELECT family_id, user_id, role, person_id, queue_mode, queue_seed,
		       queue_focus_subject_id, digest_enabled
		  FROM core.family_members
		 WHERE family_id = $1 AND user_id = $2`, familyID, userID).
		Scan(&m.FamilyID, &m.UserID, &m.Role, &m.PersonID, &m.QueueMode, &m.QueueSeed,
			&m.QueueFocusSubjectID, &m.DigestEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("membership: %w", err)
	}
	return &m, nil
}

// FamiliesOf lists what somebody belongs to, for the chooser and the switcher.
func (s *Store) FamiliesOf(ctx context.Context, userID int64) ([]Family, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT f.id, f.slug, f.display_name
		  FROM core.families f
		  JOIN core.family_members m ON m.family_id = f.id
		 WHERE m.user_id = $1
		 ORDER BY f.display_name`, userID)
	if err != nil {
		return nil, fmt.Errorf("families of user %d: %w", userID, err)
	}
	defer rows.Close()

	var out []Family
	for rows.Next() {
		var f Family
		if err := rows.Scan(&f.ID, &f.Slug, &f.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) CreateFamily(ctx context.Context, slug, displayName string) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx,
		`INSERT INTO core.families (slug, display_name) VALUES ($1, $2) RETURNING id`,
		slug, displayName).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create family %q: %w", slug, err)
	}
	return id, nil
}

func (s *Store) AddMember(ctx context.Context, familyID, userID int64, role string) error {
	return AddMemberTx(ctx, s.q(ctx), familyID, userID, role)
}

// AddMemberTx records that somebody belongs to a family, with the role they hold
// there. Package-level so the importer can call it inside its own transaction.
//
// Role lives here rather than on the user because it is only true within one
// family: the same person is an admin in theirs and a contributor in somebody
// else's.
func AddMemberTx(ctx context.Context, db DBTX, familyID, userID int64, role string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO core.family_members (family_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (family_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		familyID, userID, role)
	if err != nil {
		return fmt.Errorf("add member %d to family %d: %w", userID, familyID, err)
	}
	return nil
}
