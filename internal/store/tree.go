package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// TreePerson is one person in the pedigree, with whatever has been said about
// them attached so the tree can show where the stories are.
type TreePerson struct {
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

	// A person may belong to a couple subject, so these describe whichever
	// subject carries their questions.
	SubjectSlug   *string
	SubjectName   *string
	QuestionCount int
	AnsweredCount int

	// Filled in when the tree is assembled.
	Father *TreePerson
	Mother *TreePerson
}

// FullName follows the genealogical convention, putting the maiden name in
// parentheses between the given names and the married surname:
// "Alice Mae (Fletcher) Nash".
func (p TreePerson) FullName() string {
	if p.MarriedSurname == nil || *p.MarriedSurname == "" || *p.MarriedSurname == p.Surname {
		return strings.TrimSpace(p.Given + " " + p.Surname)
	}
	if p.Surname == "" {
		return strings.TrimSpace(p.Given + " " + *p.MarriedSurname)
	}
	return strings.TrimSpace(p.Given + " (" + p.Surname + ") " + *p.MarriedSurname)
}

// Lifespan renders "1894–1972", "b. 1958", or empty when nothing is known.
func (p TreePerson) Lifespan() string {
	switch {
	case p.BirthYear != nil && p.DeathYear != nil:
		return fmt.Sprintf("%d–%d", *p.BirthYear, *p.DeathYear)
	case p.BirthYear != nil:
		return fmt.Sprintf("b. %d", *p.BirthYear)
	case p.DeathYear != nil:
		return fmt.Sprintf("d. %d", *p.DeathYear)
	}
	return ""
}

// HasStories reports whether it is worth clicking through to this person.
func (p TreePerson) HasStories() bool { return p.AnsweredCount > 0 }

// TreePeople returns every imported person with their subject and progress.
func (s *Store) TreePeople(ctx context.Context) ([]*TreePerson, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT p.id, p.gedcom_id, p.given_name, p.surname, p.married_surname, p.sex,
		       p.birth_year, p.death_year, p.father_id, p.mother_id,
		       sub.slug, sub.display_name,
		       coalesce(counts.total, 0), coalesce(counts.answered, 0)
		FROM family.people p
		LEFT JOIN family.subject_members sm ON sm.person_id = p.id
		LEFT JOIN family.subjects sub       ON sub.id = sm.subject_id
		LEFT JOIN (
		    SELECT q.subject_id,
		           count(*) AS total,
		           count(owner.id) AS answered
		    FROM family.questions q
		    LEFT JOIN family.entries owner
		           ON owner.question_id = q.id
		          AND owner.author_user_id = q.asked_of_user_id
		          AND owner.is_draft = false
		    WHERE q.archived_at IS NULL
		    GROUP BY q.subject_id
		) counts ON counts.subject_id = sub.id
		ORDER BY p.id`)
	if err != nil {
		return nil, fmt.Errorf("tree people: %w", err)
	}
	defer rows.Close()

	var out []*TreePerson
	for rows.Next() {
		var p TreePerson
		if err := rows.Scan(&p.ID, &p.GedcomID, &p.Given, &p.Surname, &p.MarriedSurname, &p.Sex,
			&p.BirthYear, &p.DeathYear, &p.FatherID, &p.MotherID,
			&p.SubjectSlug, &p.SubjectName, &p.QuestionCount, &p.AnsweredCount); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// RootPeople returns the people the contributors correspond to — Mom and Dad —
// which are the roots the pedigree grows from.
func (s *Store) RootPeople(ctx context.Context) ([]int64, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT m.person_id
		  FROM core.family_members m
		  JOIN core.users u ON u.id = m.user_id
		 WHERE m.family_id = $1 AND m.person_id IS NOT NULL AND m.role = 'contributor'
		 ORDER BY u.display_name`, FamilyFrom(ctx))
	if err != nil {
		return nil, fmt.Errorf("root people: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SubjectMembers returns the people a subject is about, so a couple's page can
// name both of them.
func (s *Store) SubjectMembers(ctx context.Context, subjectID int64) ([]TreePerson, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT p.id, p.gedcom_id, p.given_name, p.surname, p.married_surname, p.sex,
		       p.birth_year, p.death_year, p.father_id, p.mother_id
		FROM family.subject_members sm
		JOIN family.people p ON p.id = sm.person_id
		WHERE sm.subject_id = $1
		ORDER BY p.birth_year NULLS LAST, p.id`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("subject members: %w", err)
	}
	defer rows.Close()

	var out []TreePerson
	for rows.Next() {
		var p TreePerson
		if err := rows.Scan(&p.ID, &p.GedcomID, &p.Given, &p.Surname, &p.MarriedSurname, &p.Sex,
			&p.BirthYear, &p.DeathYear, &p.FatherID, &p.MotherID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// StoriesAboutSubject returns stories explicitly tied to a subject, so a person's
// page gathers everything said about them in one place.
func (s *Store) StoriesAboutSubject(ctx context.Context, subjectID, viewerID int64) ([]Story, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT `+storyColumns+storyJoins+`
		  AND e.subject_id = $1
		  AND (e.is_draft = false OR e.author_user_id = $2)
		ORDER BY e.created_at DESC`, subjectID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("stories about subject: %w", err)
	}
	defer rows.Close()

	var out []Story
	for rows.Next() {
		st, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// SubjectProgressBySlug is the header for a subject page.
func (s *Store) SubjectProgressBySlug(ctx context.Context, slug string) (*SubjectProgress, error) {
	var p SubjectProgress
	err := s.q(ctx).QueryRow(ctx, `
		SELECT s.id, s.slug, s.kind, s.display_name, s.sort_order,
		       count(q.id), count(owner.id)
		FROM family.subjects s
		LEFT JOIN family.questions q
		       ON q.subject_id = s.id AND q.archived_at IS NULL
		LEFT JOIN family.entries owner
		       ON owner.question_id = q.id
		      AND owner.author_user_id = q.asked_of_user_id
		      AND owner.is_draft = false
		WHERE s.slug = $1
		GROUP BY s.id, s.slug, s.kind, s.display_name, s.sort_order`, slug).
		Scan(&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.SortOrder, &p.Total, &p.Answered)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
