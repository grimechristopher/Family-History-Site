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
	// Generation is how far back from the contributors: 0 one of them, 1 a parent,
	// 2 a grandparent. Used to group the sidebar.
	Generation int
}

// UpsertPerson inserts or updates by GEDCOM xref and returns the row id. Parent
// links are set separately, because a parent may not exist yet on first pass.
func UpsertPerson(ctx context.Context, db DBTX, p Person) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO family.people
		  (gedcom_id, given_name, surname, married_surname, sex, birth_year, death_year, family_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (family_id, gedcom_id) DO UPDATE SET
		  given_name      = EXCLUDED.given_name,
		  surname         = EXCLUDED.surname,
		  married_surname = EXCLUDED.married_surname,
		  sex             = EXCLUDED.sex,
		  birth_year      = EXCLUDED.birth_year,
		  death_year      = EXCLUDED.death_year
		RETURNING id`,
		p.GedcomID, p.Given, p.Surname, p.MarriedSurname, p.Sex, p.BirthYear, p.DeathYear, FamilyFrom(ctx)).Scan(&id)
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
		INSERT INTO family.subjects (slug, kind, display_name, sort_order, family_id, generation)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (family_id, slug) DO UPDATE SET
		  kind         = EXCLUDED.kind,
		  display_name = EXCLUDED.display_name,
		  sort_order   = EXCLUDED.sort_order,
		  generation   = EXCLUDED.generation
		RETURNING id`, s.Slug, s.Kind, s.DisplayName, s.SortOrder, FamilyFrom(ctx),
		s.Generation).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert subject %s: %w", s.Slug, err)
	}
	return id, nil
}

// SetSubjectMembers replaces the membership of a subject wholesale, so a
// re-import cannot leave a stale member behind.
func SetSubjectMembers(ctx context.Context, db DBTX, subjectID int64, personIDs []int64) error {
	// subject_id already belongs to exactly one family, so this is safe either way.
	// Named anyway, so that every broad statement in this package reads the same and
	// none of them depends on row-level security being in force.
	if _, err := db.Exec(ctx,
		`DELETE FROM family.subject_members WHERE subject_id = $1 AND family_id = $2`,
		subjectID, FamilyFrom(ctx)); err != nil {
		return fmt.Errorf("clear members of subject %d: %w", subjectID, err)
	}
	for _, personID := range personIDs {
		_, err := db.Exec(ctx, `
			INSERT INTO family.subject_members (subject_id, person_id, family_id)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, subjectID, personID, FamilyFrom(ctx))
		if err != nil {
			return fmt.Errorf("add person %d to subject %d: %w", personID, subjectID, err)
		}
	}
	return nil
}

func (s *Store) SubjectBySlug(ctx context.Context, slug string) (*Subject, error) {
	var sub Subject
	err := s.q(ctx).QueryRow(ctx,
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
	rows, err := s.q(ctx).Query(ctx,
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
	err := s.q(ctx).QueryRow(ctx, `SELECT count(*) FROM family.people`).Scan(&n)
	return n, err
}

// PruneSubjectsNotIn removes subjects this family no longer derives.
//
// Re-importing has always updated and added, never removed, so a subject that
// falls out of scope stayed for ever: splitting a family in two left one side
// holding the other's ancestors, and renaming somebody left the old name behind.
//
// Only rows with nothing attached are deleted. A subject that has questions or a
// story is left alone and counted, because losing what somebody wrote is worse
// than a stale name in a list -- the caller reports it rather than deciding.
func PruneSubjectsNotIn(ctx context.Context, db DBTX, slugs []string) (deleted, kept int64, err error) {
	row := db.QueryRow(ctx, `
		WITH stale AS (
			SELECT s.id,
			       -- An archived question is one this import no longer produces, so
			       -- it is not a reason to keep the subject. Only a live question or
			       -- something somebody wrote counts.
			       EXISTS (SELECT 1 FROM family.questions q
			                WHERE q.subject_id = s.id AND q.archived_at IS NULL) AS has_questions,
			       EXISTS (SELECT 1 FROM family.entries e WHERE e.subject_id = s.id) AS has_entries
			  FROM family.subjects s
			 WHERE s.family_id = $2 AND NOT (s.slug = ANY($1::text[]))
		),
		-- Their archived questions go with them: nothing was ever answered, and
		-- leaving orphans behind is what made the counts confusing.
		gone_questions AS (
			DELETE FROM family.questions
			 WHERE subject_id IN (SELECT id FROM stale WHERE NOT has_questions AND NOT has_entries)
			RETURNING 1
		),
		gone AS (
			DELETE FROM family.subjects
			 WHERE id IN (SELECT id FROM stale WHERE NOT has_questions AND NOT has_entries)
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM gone),
		       (SELECT count(*) FROM stale WHERE has_questions OR has_entries)`,
		slugs, FamilyFrom(ctx))
	if err := row.Scan(&deleted, &kept); err != nil {
		return 0, 0, fmt.Errorf("prune subjects: %w", err)
	}
	return deleted, kept, nil
}

// PrunePeopleNotIn removes people no longer inside the imported window, for the
// same reason and with the same caution: anyone still referenced is left.
func PrunePeopleNotIn(ctx context.Context, db DBTX, gedcomIDs []string) (int64, error) {
	// People point at their parents, so anyone still referenced cannot simply be
	// deleted. The references are cleared first: whoever survives keeps their own
	// parents, and a link to somebody who is leaving becomes nothing rather than
	// blocking the whole prune.
	for _, column := range []string{"father_id", "mother_id"} {
		_, err := db.Exec(ctx, `
			UPDATE family.people SET `+column+` = NULL
			 WHERE family_id = $2
			   AND `+column+` IN (
			       SELECT id FROM family.people
			        WHERE family_id = $2 AND NOT (gedcom_id = ANY($1::text[])))`,
			gedcomIDs, FamilyFrom(ctx))
		if err != nil {
			return 0, fmt.Errorf("clear %s before pruning people: %w", column, err)
		}
	}

	tag, err := db.Exec(ctx, `
		DELETE FROM family.people p
		 WHERE p.family_id = $2
		   AND NOT (p.gedcom_id = ANY($1::text[]))
		   AND NOT EXISTS (SELECT 1 FROM family.subject_members sm WHERE sm.person_id = p.id)
		   AND NOT EXISTS (SELECT 1 FROM core.family_members m
		                    WHERE m.family_id = p.family_id AND m.person_id = p.id)`,
		gedcomIDs, FamilyFrom(ctx))
	if err != nil {
		return 0, fmt.Errorf("prune people: %w", err)
	}
	return tag.RowsAffected(), nil
}
