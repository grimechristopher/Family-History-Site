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
		 WHERE family_id = $1 AND user_id = $2 AND removed_at IS NULL`, familyID, userID).
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
		 WHERE m.user_id = $1 AND m.removed_at IS NULL
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
	// removed_at is cleared, so adding somebody back is bringing them back rather
	// than leaving a membership that exists and does not work.
	//
	// askable only seeds the row on first insert -- it's left out of the ON
	// CONFLICT SET, so re-adding somebody or changing their role does not undo a
	// flag already flipped by hand on /people.
	_, err := db.Exec(ctx, `
		INSERT INTO core.family_members (family_id, user_id, role, askable)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (family_id, user_id) DO UPDATE
		   SET role = EXCLUDED.role, removed_at = NULL`,
		familyID, userID, role, role != RoleAdmin)
	if err != nil {
		return fmt.Errorf("add member %d to family %d: %w", userID, familyID, err)
	}
	return nil
}

// Member is somebody in a family, as the people page shows them.
type Member struct {
	UserID      int64
	DisplayName string
	Email       string
	Role        string
	PersonID    *int64 // who they are on the tree, nil when not linked
	PersonName  string
	// Written is how many answers and stories they have contributed to this line.
	// Removing somebody who has written nothing is housekeeping; removing somebody
	// who has written is a thing to say out loud first.
	Written int
	// Askable is whether they can be offered as an answer to "who should answer
	// it?" -- independent of role. A contributor defaults to true and an admin to
	// false, but either can be flipped by hand.
	Askable bool
	// Removed marks a membership that has ended. The row stays because everything
	// they wrote hangs off it.
	Removed bool
}

// Members lists everybody in this family, in the order they joined, so the page
// reads as a history of who was added rather than an alphabetical roster.
func (s *Store) Members(ctx context.Context, familyID int64) ([]Member, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT u.id, u.display_name, u.email, m.role, m.person_id,
		       coalesce(trim(p.given_name || ' ' || p.surname), ''),
		       -- What they have written, which decides how removing them reads.
		       (SELECT count(*) FROM family.entries e
		         WHERE e.author_user_id = u.id AND e.family_id = m.family_id),
		       m.askable
		  FROM core.family_members m
		  JOIN core.users u ON u.id = m.user_id
		  LEFT JOIN family.people p ON p.id = m.person_id AND p.family_id = m.family_id
		 WHERE m.family_id = $1 AND m.removed_at IS NULL
		 ORDER BY m.created_at`, familyID)
	if err != nil {
		return nil, fmt.Errorf("members: %w", err)
	}
	defer rows.Close()

	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.PersonID,
			&m.PersonName, &m.Written, &m.Askable); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UnclaimedTreePeople are the living people in this family's tree who are not already
// somebody's account, for the picker when adding a member. Offering a person who
// is already claimed would let two accounts be the same person.
// Scoped to one family, because the picker sits under a form that adds somebody
// to that family: offering Ashley's grandmother while adding a member of the
// Grime line is a choice that could only ever be a mistake.
func (s *Store) UnclaimedTreePeople(ctx context.Context, familyID int64) ([]TreePerson, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT p.id, p.given_name, p.surname, p.married_surname, p.birth_year
		  FROM family.people p
		 WHERE p.family_id = $1
		   AND NOT EXISTS (
		       SELECT 1 FROM core.family_members m
		        WHERE m.family_id = p.family_id AND m.person_id = p.id
		          AND m.removed_at IS NULL)
		   -- People who could actually sign in. A death year settles it; so does a
		   -- birth year old enough that no answer is coming either way. Somebody
		   -- with neither recorded is still offered, because plenty of living
		   -- relatives have no dates in the file.
		   AND p.death_year IS NULL
		   AND (p.birth_year IS NULL
		        OR p.birth_year > extract(year FROM now())::int - 110)
		 ORDER BY p.surname, p.given_name`, familyID)
	if err != nil {
		return nil, fmt.Errorf("unclaimed tree people: %w", err)
	}
	defer rows.Close()

	var out []TreePerson
	for rows.Next() {
		var p TreePerson
		if err := rows.Scan(&p.ID, &p.Given, &p.Surname, &p.MarriedSurname, &p.BirthYear); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetMemberPerson records which person in the tree a member is.
//
// A method rather than the package-level LinkUserToPerson because it has to run on
// the request's transaction: the membership it updates may have been inserted
// moments earlier in that same transaction, and a second connection cannot see it
// yet. Handing this the pool instead updates nothing and reports success.
func (s *Store) SetMemberPerson(ctx context.Context, familyID, userID int64, personID *int64) error {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE core.family_members SET person_id = $3
		 WHERE family_id = $1 AND user_id = $2`, familyID, userID, personID)
	if err != nil {
		return fmt.Errorf("link user %d to person: %w", userID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("link user %d to person: they are not a member of family %d",
			userID, familyID)
	}
	return nil
}

// UpsertUserIn creates or updates an identity on the request's transaction.
func (s *Store) UpsertUserIn(ctx context.Context, email, displayName string) (int64, error) {
	return UpsertUser(ctx, s.q(ctx), email, displayName)
}

// MemberByDisplayName finds somebody by the name they are shown under, within one
// family. Names are only unique inside a family -- every family has a "Dad" -- so
// looking one up without saying which family returns whichever row Postgres
// happens to yield first.
func (s *Store) MemberByDisplayName(ctx context.Context, familyID int64, name string) (*User, error) {
	var u User
	err := s.q(ctx).QueryRow(ctx, `
		SELECT u.id, u.email, u.supabase_user_id, u.display_name
		  FROM core.users u
		  JOIN core.family_members m ON m.user_id = u.id
		 WHERE m.family_id = $1 AND m.removed_at IS NULL
		   AND u.display_name = $2`, familyID, name).
		Scan(&u.ID, &u.Email, &u.SupabaseUserID, &u.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("member %q of family %d: %w", name, familyID, err)
	}
	return &u, nil
}

// MemberNames lists who is in a family, for error messages that offer the
// alternatives rather than a bare failure.
func (s *Store) MemberNames(ctx context.Context, familyID int64) ([]string, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT u.display_name FROM core.users u
		  JOIN core.family_members m ON m.user_id = u.id
		 WHERE m.family_id = $1 AND m.removed_at IS NULL
		 ORDER BY u.display_name`, familyID)
	if err != nil {
		return nil, fmt.Errorf("member names: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// StandingOf is what somebody is across all the families they belong to.
//
// A person has a role in each family, and with one combined view there has to be
// one answer: admin anywhere makes them an admin here, because the only thing the
// role gates is seeing everybody's questions rather than their own. The queue
// settings come from whichever membership holds them -- they are kept in step, so
// the card stack behaves the same wherever it is read.
type Standing struct {
	Role                string
	QueueMode           string
	QueueSeed           int64
	QueueFocusSubjectID *int64
	PersonID            *int64
}

func (s *Store) StandingOf(ctx context.Context, userID int64) (Standing, error) {
	var st Standing
	err := s.q(ctx).QueryRow(ctx, `
		SELECT CASE WHEN bool_or(role = 'admin') THEN 'admin' ELSE 'contributor' END,
		       coalesce(min(queue_mode), 'all'),
		       coalesce(min(queue_seed), 0),
		       (array_agg(queue_focus_subject_id) FILTER (WHERE queue_focus_subject_id IS NOT NULL))[1],
		       (array_agg(person_id) FILTER (WHERE person_id IS NOT NULL))[1]
		  FROM core.family_members WHERE user_id = $1 AND removed_at IS NULL`, userID).
		Scan(&st.Role, &st.QueueMode, &st.QueueSeed, &st.QueueFocusSubjectID, &st.PersonID)
	if err != nil {
		return st, fmt.Errorf("standing of user %d: %w", userID, err)
	}
	return st, nil
}

// HomeLine is the line somebody's chart should open on: the one they are actually
// in, rather than whichever happened to be drawn first.
//
// Preferring the line where they are a contributor, then any where they are
// somebody in the tree. An admin who belongs to four lines and is nobody's
// ancestor has no home line, and gets an empty string -- there is no honest answer
// for them, and the chart falls back to remembering their last choice.
func (s *Store) HomeLine(ctx context.Context, userID int64) (string, error) {
	var slug string
	err := s.q(ctx).QueryRow(ctx, `
		SELECT f.slug
		  FROM core.family_members m
		  JOIN core.families f ON f.id = m.family_id
		 WHERE m.user_id = $1 AND m.family_id = ANY($2) AND m.removed_at IS NULL
		 ORDER BY (m.role = 'contributor') DESC,
		          (m.person_id IS NOT NULL) DESC,
		          f.slug
		 LIMIT 1`, userID, FamilyIDsFrom(ctx)).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("home line: %w", err)
	}
	return slug, nil
}

// RemoveMember takes somebody out of a family without touching what they wrote.
//
// Their answers and stories stay, under their name. Somebody who has told the
// family about their grandmother does not stop having told them because their
// address was a mistake or they have left; the record is the whole point, and it
// is not the site's to delete.
//
// The account itself stays too, because it may belong to another line. It simply
// no longer has any way into this one: membership is what every policy is written
// against, so removing it removes the access.
func (s *Store) RemoveMember(ctx context.Context, familyID, userID int64) error {
	// Out of their card stack. A question put to four people stays for the other
	// three; one put only to this person is now asked of nobody, and stays exactly
	// where it is.
	//
	// It used to be archived at that point, which was too strong by a long way.
	// Removing the last person from a line archived all 107 of its questions at
	// once -- recoverable, but indistinguishable from having destroyed the line.
	// A question nobody is asked is still a question about somebody's grandmother;
	// it waits until there is somebody to put it to. Archiving is for questions the
	// prompts file no longer contains, which is a different thing entirely.
	if _, err := s.q(ctx).Exec(ctx, `
		DELETE FROM family.question_askees a
		 USING family.questions q
		 WHERE q.id = a.question_id AND q.family_id = $1 AND a.user_id = $2`,
		familyID, userID); err != nil {
		return fmt.Errorf("remove from stacks: %w", err)
	}

	// The membership is ended rather than deleted. Every answer, reply and
	// photograph carries a foreign key to it -- that is what guarantees an author
	// really is in the family the row belongs to -- so deleting it would mean
	// deleting everything they wrote, which is the one thing this must not do.
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE core.family_members SET removed_at = now()
		 WHERE family_id = $1 AND user_id = $2 AND removed_at IS NULL`,
		familyID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMemberEmail changes the address somebody signs in with.
//
// The address is on the account rather than the membership, so this changes it
// everywhere they are. That is right -- it is one person with one inbox -- but it
// means a family cannot quietly change how somebody signs in to another family, so
// the caller checks they share a line first.
func (s *Store) SetMemberEmail(ctx context.Context, userID int64, email string) error {
	tag, err := s.q(ctx).Exec(ctx,
		`UPDATE core.users SET email = $2 WHERE id = $1`, userID, email)
	if err != nil {
		return fmt.Errorf("set member email: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Member is one person's place in one family, with what they have written.
// Looked up rather than filtered out of Members, so a handler acting on somebody
// is acting on a row it fetched by id and not on a name it was handed.
func (s *Store) Member(ctx context.Context, familyID, userID int64) (*Member, error) {
	var m Member
	err := s.q(ctx).QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, m.role, m.person_id,
		       coalesce(trim(p.given_name || ' ' || p.surname), ''),
		       (SELECT count(*) FROM family.entries e
		         WHERE e.author_user_id = u.id AND e.family_id = m.family_id),
		       m.askable
		  FROM core.family_members m
		  JOIN core.users u ON u.id = m.user_id
		  LEFT JOIN family.people p ON p.id = m.person_id AND p.family_id = m.family_id
		 WHERE m.family_id = $1 AND m.user_id = $2 AND m.removed_at IS NULL`, familyID, userID).
		Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.PersonID, &m.PersonName,
			&m.Written, &m.Askable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("member: %w", err)
	}
	return &m, nil
}

// MemberByNameAnyState finds somebody in a family by the name they are shown
// under, whether or not their membership has ended.
//
// Adding is keyed on an email address, so somebody added back under a new address
// becomes a second account with the same name -- a second Robert Lucero standing
// beside the first, with the questions still attached to the one who was there
// before. This is how the add path notices and says so instead.
func (s *Store) MemberByNameAnyState(ctx context.Context, familyID int64, name string) (*Member, error) {
	var m Member
	err := s.q(ctx).QueryRow(ctx, `
		SELECT u.id, u.display_name, u.email, m.role, m.person_id,
		       coalesce(trim(p.given_name || ' ' || p.surname), ''), 0,
		       m.removed_at IS NOT NULL
		  FROM core.family_members m
		  JOIN core.users u ON u.id = m.user_id
		  LEFT JOIN family.people p ON p.id = m.person_id AND p.family_id = m.family_id
		 WHERE m.family_id = $1 AND lower(u.display_name) = lower($2)
		 ORDER BY m.removed_at NULLS FIRST
		 LIMIT 1`, familyID, name).
		Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.PersonID, &m.PersonName,
			&m.Written, &m.Removed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("member by name: %w", err)
	}
	return &m, nil
}
