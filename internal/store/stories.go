package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Story is a free-form entry with no question attached — for a memory that
// surfaces when no prompt covers it.
//
// Stories share the entries table with answers: question_id IS NULL means story.
// One table means replies, photos, and attribution are written once and work for
// both.
type Story struct {
	ID           int64
	AuthorUserID int64
	AuthorName   string
	Title        string
	Body         string
	SubjectID    *int64
	SubjectName  *string
	IsDraft      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ReplyCount   int
}

// familyID is used when the story is about nobody in particular. A story attached
// to a subject takes that subject's family instead, which is the only value the
// composite foreign key would accept anyway.
func (s *Store) CreateStory(ctx context.Context, authorUserID int64, title, body string, subjectID *int64, isDraft bool, familyID int64) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.entries (question_id, author_user_id, title, body, subject_id, is_draft, family_id)
		VALUES (NULL, $1, $2, $3, $4, $5,
		        coalesce((SELECT family_id FROM family.subjects WHERE id = $4), $6))
		RETURNING id`, authorUserID, title, body, subjectID, isDraft, familyID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create story: %w", err)
	}
	return id, nil
}

func (s *Store) UpdateStory(ctx context.Context, storyID int64, title, body string, subjectID *int64, isDraft bool) error {
	_, err := s.q(ctx).Exec(ctx, `
		UPDATE family.entries
		SET title = $2, body = $3, subject_id = $4, is_draft = $5, updated_at = now()
		WHERE id = $1 AND question_id IS NULL`, storyID, title, body, subjectID, isDraft)
	if err != nil {
		return fmt.Errorf("update story %d: %w", storyID, err)
	}
	return nil
}

const storyColumns = `
	e.id, e.author_user_id, u.display_name, coalesce(e.title, ''), e.body,
	e.subject_id, s.display_name, e.is_draft, e.created_at, e.updated_at,
	coalesce(r.n, 0)`

func scanStory(row pgx.Row) (*Story, error) {
	var st Story
	err := row.Scan(&st.ID, &st.AuthorUserID, &st.AuthorName, &st.Title, &st.Body,
		&st.SubjectID, &st.SubjectName, &st.IsDraft, &st.CreatedAt, &st.UpdatedAt,
		&st.ReplyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

const storyJoins = `
	FROM family.entries e
	JOIN core.users u ON u.id = e.author_user_id
	LEFT JOIN family.subjects s ON s.id = e.subject_id
	LEFT JOIN (SELECT entry_id, count(*) AS n FROM family.replies GROUP BY entry_id) r
	       ON r.entry_id = e.id
	WHERE e.question_id IS NULL`

// ListStories returns published stories, newest first. A viewer's own drafts are
// included so an unfinished story is never lost from view.
func (s *Store) ListStories(ctx context.Context, viewerID int64) ([]Story, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT `+storyColumns+storyJoins+`
		  AND (e.is_draft = false OR e.author_user_id = $1)
		ORDER BY e.created_at DESC`, viewerID)
	if err != nil {
		return nil, fmt.Errorf("list stories: %w", err)
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

func (s *Store) Story(ctx context.Context, storyID int64) (*Story, error) {
	return scanStory(s.q(ctx).QueryRow(ctx,
		`SELECT `+storyColumns+storyJoins+` AND e.id = $1`, storyID))
}

func (s *Store) DeleteStory(ctx context.Context, storyID int64) error {
	_, err := s.q(ctx).Exec(ctx,
		`DELETE FROM family.entries WHERE id = $1 AND question_id IS NULL`, storyID)
	return err
}
