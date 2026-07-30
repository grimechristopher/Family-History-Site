package store

import (
	"context"
	"fmt"
)

// ImportedQuestion is a question sourced from the prompts markdown.
type ImportedQuestion struct {
	SubjectID     int64
	AskedOfUserID int64
	Topic         *string
	Body          string
	SortOrder     int
	IsProposed    bool
	ImportKey     string
}

// UpsertImportedQuestion keys on import_key, which encodes position rather than
// content. Rewording a question in Obsidian therefore updates the existing row
// instead of creating a duplicate and orphaning its answers.
func UpsertImportedQuestion(ctx context.Context, db DBTX, q ImportedQuestion) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, `
		INSERT INTO family.questions
		  (subject_id, asked_of_user_id, topic, body, sort_order, is_proposed, source, import_key)
		VALUES ($1, $2, $3, $4, $5, $6, 'import', $7)
		ON CONFLICT (import_key) DO UPDATE SET
		  subject_id       = EXCLUDED.subject_id,
		  asked_of_user_id = EXCLUDED.asked_of_user_id,
		  topic            = EXCLUDED.topic,
		  body             = EXCLUDED.body,
		  sort_order       = EXCLUDED.sort_order,
		  is_proposed      = EXCLUDED.is_proposed,
		  archived_at      = NULL
		RETURNING id`,
		q.SubjectID, q.AskedOfUserID, q.Topic, q.Body, q.SortOrder, q.IsProposed, q.ImportKey).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert question %s: %w", q.ImportKey, err)
	}
	return id, nil
}

// ArchiveImportedQuestionsNotIn marks imported questions whose import_key no
// longer appears in the markdown. They are archived rather than deleted, because
// an answer may already hang off one and deleting it would destroy writing.
func ArchiveImportedQuestionsNotIn(ctx context.Context, db DBTX, keys []string) (int64, error) {
	tag, err := db.Exec(ctx, `
		UPDATE family.questions
		SET archived_at = now()
		WHERE source = 'import'
		  AND archived_at IS NULL
		  AND NOT (import_key = ANY($1::text[]))`, keys)
	if err != nil {
		return 0, fmt.Errorf("archive removed questions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) CountQuestions(ctx context.Context) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM family.questions WHERE archived_at IS NULL`).Scan(&n)
	return n, err
}

func (s *Store) CountQuestionsFor(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM family.questions
		 WHERE asked_of_user_id = $1 AND archived_at IS NULL`, userID).Scan(&n)
	return n, err
}

// CreateUserQuestion adds a question written on the site rather than imported
// from the markdown.
//
// It carries no import_key, so a re-import never touches it, and records who
// wrote it: a question Chris asks his father should read differently from one
// that came out of the prompts file.
func (s *Store) CreateUserQuestion(ctx context.Context, subjectID, askedOfUserID, authorID int64, topic *string, body string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO family.questions
		  (subject_id, asked_of_user_id, topic, body, sort_order, source, created_by_user_id)
		VALUES ($1, $2, $3, $4,
		        coalesce((SELECT max(sort_order) + 1 FROM family.questions), 0),
		        'user', $5)
		RETURNING id`, subjectID, askedOfUserID, topic, body, authorID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create question: %w", err)
	}
	return id, nil
}
