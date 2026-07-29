package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type Person struct {
	ID             int64
	GedcomID       string
	Given          string
	Surname        string
	MarriedSurname *string
	Sex            *string
	BirthYear      *int
	DeathYear      *int
	FatherID       *int64
	MotherID       *int64
}

type Subject struct {
	ID          int64
	Slug        string
	Kind        string
	DisplayName string
	SortOrder   int
}

// UpsertPerson inserts or updates by GEDCOM xref and returns the row id. Parent
// links are set separately, because a parent may not exist yet on first pass.
func UpsertPerson(ctx context.Context, db DBTX, p Person) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO family.people
		  (gedcom_id, given_name, surname, married_surname, sex, birth_year, death_year)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (gedcom_id) DO UPDATE SET
		  given_name      = EXCLUDED.given_name,
		  surname         = EXCLUDED.surname,
		  married_surname = EXCLUDED.married_surname,
		  sex             = EXCLUDED.sex,
		  birth_year      = EXCLUDED.birth_year,
		  death_year      = EXCLUDED.death_year
		RETURNING id`,
		p.GedcomID, p.Given, p.Surname, p.MarriedSurname, p.Sex, p.BirthYear, p.DeathYear).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert person %s: %w", p.GedcomID, err)
	}
	return id, nil
}

func SetParents(ctx context.Context, db DBTX, personID int64, fatherID, motherID *int64) error {
	_, err := db.Exec(ctx,
		`UPDATE family.people SET father_id = $2, mother_id = $3 WHERE id = $1`,
		personID, fatherID, motherID)
	if err != nil {
		return fmt.Errorf("set parents for person %d: %w", personID, err)
	}
	return nil
}

func UpsertSubject(ctx context.Context, db DBTX, s Subject) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO family.subjects (slug, kind, display_name, sort_order)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (slug) DO UPDATE SET
		  kind         = EXCLUDED.kind,
		  display_name = EXCLUDED.display_name,
		  sort_order   = EXCLUDED.sort_order
		RETURNING id`, s.Slug, s.Kind, s.DisplayName, s.SortOrder).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert subject %s: %w", s.Slug, err)
	}
	return id, nil
}

// SetSubjectMembers replaces the membership of a subject wholesale, so a
// re-import cannot leave a stale member behind.
func SetSubjectMembers(ctx context.Context, db DBTX, subjectID int64, personIDs []int64) error {
	if _, err := db.Exec(ctx,
		`DELETE FROM family.subject_members WHERE subject_id = $1`, subjectID); err != nil {
		return fmt.Errorf("clear members of subject %d: %w", subjectID, err)
	}
	for _, personID := range personIDs {
		_, err := db.Exec(ctx, `
			INSERT INTO family.subject_members (subject_id, person_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, subjectID, personID)
		if err != nil {
			return fmt.Errorf("add person %d to subject %d: %w", personID, subjectID, err)
		}
	}
	return nil
}

func (s *Store) SubjectBySlug(ctx context.Context, slug string) (*Subject, error) {
	var sub Subject
	err := s.Pool.QueryRow(ctx,
		`SELECT id, slug, kind, display_name, sort_order FROM family.subjects WHERE slug = $1`,
		slug).Scan(&sub.ID, &sub.Slug, &sub.Kind, &sub.DisplayName, &sub.SortOrder)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sub, nil
}

func (s *Store) Subjects(ctx context.Context) ([]Subject, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id, slug, kind, display_name, sort_order
		 FROM family.subjects ORDER BY sort_order, slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Subject
	for rows.Next() {
		var sub Subject
		if err := rows.Scan(&sub.ID, &sub.Slug, &sub.Kind, &sub.DisplayName, &sub.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) CountPeople(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM family.people`).Scan(&n)
	return n, err
}
