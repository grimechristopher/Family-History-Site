package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Entry struct {
	ID           int64
	QuestionID   *int64
	SubjectID    *int64
	AuthorUserID int64
	AuthorName   string
	Title        *string
	Body         string
	IsDraft      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// SaveAnswer creates or replaces a person's answer to a question.
//
// isDraft is always explicit: autosave passes true, the save action passes false.
// The column has no default so neither call site can omit it and leave an answer
// silently uncounted.
func (s *Store) SaveAnswer(ctx context.Context, questionID, authorUserID int64, body string, isDraft bool) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.entries (question_id, author_user_id, body, is_draft, family_id)
		VALUES ($1, $2, $3, $4, (SELECT family_id FROM family.questions WHERE id = $1))
		ON CONFLICT (question_id, author_user_id) DO UPDATE SET
		  body       = EXCLUDED.body,
		  is_draft   = EXCLUDED.is_draft,
		  updated_at = now()
		RETURNING id`, questionID, authorUserID, body, isDraft).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save answer to question %d: %w", questionID, err)
	}
	return id, nil
}

func (s *Store) AnswerFor(ctx context.Context, questionID, authorUserID int64) (*Entry, error) {
	var e Entry
	err := s.q(ctx).QueryRow(ctx, `
		SELECT id, question_id, subject_id, author_user_id, title, body, is_draft,
		       created_at, updated_at
		FROM family.entries
		WHERE question_id = $1 AND author_user_id = $2`,
		questionID, authorUserID).Scan(&e.ID, &e.QuestionID, &e.SubjectID, &e.AuthorUserID,
		&e.Title, &e.Body, &e.IsDraft, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// AnswersTo returns every published answer to a question, the intended person's
// first so the template can render the primary answer above the "Others" section.
func (s *Store) AnswersTo(ctx context.Context, questionID int64) ([]Entry, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT e.id, e.question_id, e.subject_id, e.author_user_id, u.display_name,
		       e.title, e.body, e.is_draft, e.created_at, e.updated_at
		FROM family.entries e
		JOIN core.users u ON u.id = e.author_user_id
		JOIN family.questions q ON q.id = e.question_id
		WHERE e.question_id = $1 AND e.is_draft = false
		ORDER BY (e.author_user_id = q.asked_of_user_id) DESC, e.created_at`,
		questionID)
	if err != nil {
		return nil, fmt.Errorf("answers to question %d: %w", questionID, err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.QuestionID, &e.SubjectID, &e.AuthorUserID, &e.AuthorName,
			&e.Title, &e.Body, &e.IsDraft, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QuestionDetail is a question plus the context needed to render it.
type QuestionDetail struct {
	ID            int64
	Body          string
	Topic         *string
	SubjectName   string
	SubjectSlug   string
	AskedOfUserID int64
	AskedOfName   string
}

func (s *Store) Question(ctx context.Context, questionID int64) (*QuestionDetail, error) {
	var q QuestionDetail
	err := s.q(ctx).QueryRow(ctx, `
		SELECT q.id, q.body, q.topic, s.display_name, s.slug, q.asked_of_user_id, u.display_name
		FROM family.questions q
		JOIN family.subjects s ON s.id = q.subject_id
		JOIN core.users u ON u.id = q.asked_of_user_id
		WHERE q.id = $1 AND q.archived_at IS NULL`, questionID).
		Scan(&q.ID, &q.Body, &q.Topic, &q.SubjectName, &q.SubjectSlug, &q.AskedOfUserID, &q.AskedOfName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}
