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
// TreeRoot is the person a line is drawn from, and the line's name.
type TreeRoot struct {
	PersonID   int64
	FamilySlug string
	FamilyName string
}

// RootPeople returns one root per family: the contributor whose ancestors that
// line actually is.
//
// One per family matters because somebody may be a member of several. Lori is in
// her husband's line as his wife and in her own as its root, and drawing a chart
// from each membership showed her twice -- once properly, once as a lone box with
// no ancestors above it. Preferring the person who has a parent recorded picks the
// line's own root; the fallback keeps a family with no recorded parents drawable.
func (s *Store) RootPeople(ctx context.Context) ([]TreeRoot, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT DISTINCT ON (m.family_id) m.person_id, f.slug, f.display_name
		  FROM core.family_members m
		  JOIN core.families f ON f.id = m.family_id
		  JOIN family.people p ON p.id = m.person_id
		 WHERE m.family_id = ANY($1)
		   AND m.role = 'contributor'
		   AND m.person_id IS NOT NULL
		 ORDER BY m.family_id,
		          (p.father_id IS NOT NULL OR p.mother_id IS NOT NULL) DESC,
		          p.id`, FamilyIDsFrom(ctx))
	if err != nil {
		return nil, fmt.Errorf("root people: %w", err)
	}
	defer rows.Close()

	var out []TreeRoot
	for rows.Next() {
		var r TreeRoot
		if err := rows.Scan(&r.PersonID, &r.FamilySlug, &r.FamilyName); err != nil {
			return nil, err
		}
		out = append(out, r)
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
//
// familySlug disambiguates: a subject slug is unique inside a line and not across
// them, so every line has a "further-back" and a lookup by slug alone returned
// whichever row came back first. Empty means any line the viewer belongs to,
// which is right for somebody in one and a coin toss for somebody in four -- so
// every link that can name the line does.
func (s *Store) SubjectProgressBySlug(ctx context.Context, slug, familySlug string) (*SubjectProgress, error) {
	var p SubjectProgress
	err := s.q(ctx).QueryRow(ctx, `
		SELECT s.id, s.slug, s.kind, s.display_name, s.sort_order,
		       f.slug, f.display_name,
		       count(q.id), count(owner.id)
		FROM family.subjects s
		JOIN core.families f ON f.id = s.family_id
		LEFT JOIN family.questions q
		       ON q.subject_id = s.id AND q.archived_at IS NULL
		LEFT JOIN family.entries owner
		       ON owner.question_id = q.id
		      AND owner.author_user_id = q.asked_of_user_id
		      AND owner.is_draft = false
		WHERE s.slug = $1 AND ($2 = '' OR f.slug = $2)
		GROUP BY s.id, s.slug, s.kind, s.display_name, s.sort_order,
		         f.slug, f.display_name`, slug, familySlug).
		Scan(&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.SortOrder,
			&p.FamilySlug, &p.FamilyName, &p.Total, &p.Answered)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
