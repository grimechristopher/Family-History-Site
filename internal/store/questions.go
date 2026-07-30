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
		  (subject_id, asked_of_user_id, topic, body, sort_order, is_proposed, source, import_key, family_id)
		VALUES ($1, $2, $3, $4, $5, $6, 'import', $7, $8)
		ON CONFLICT (family_id, import_key) DO UPDATE SET
		  subject_id       = EXCLUDED.subject_id,
		  asked_of_user_id = EXCLUDED.asked_of_user_id,
		  topic            = EXCLUDED.topic,
		  body             = EXCLUDED.body,
		  sort_order       = EXCLUDED.sort_order,
		  is_proposed      = EXCLUDED.is_proposed,
		  archived_at      = NULL
		RETURNING id`,
		q.SubjectID, q.AskedOfUserID, q.Topic, q.Body, q.SortOrder, q.IsProposed, q.ImportKey,
		FamilyFrom(ctx)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert question %s: %w", q.ImportKey, err)
	}
	// Recorded here rather than left to the caller. Being asked lives in
	// question_askees now, and a question with nobody attached is in no card stack
	// at all -- a silence that looks exactly like having answered everything.
	if err := AddAskee(ctx, db, id, q.AskedOfUserID); err != nil {
		return 0, err
	}
	return id, nil
}

// ArchiveImportedQuestionsNotIn marks imported questions whose import_key no
// longer appears in the markdown. They are archived rather than deleted, because
// an answer may already hang off one and deleting it would destroy writing.
func ArchiveImportedQuestionsNotIn(ctx context.Context, db DBTX, keys []string) (int64, error) {
	// family_id is named explicitly rather than left to row-level security. This
	// statement archives everything it does not recognise, so if it ever runs with
	// the family unset -- or as a role that is exempt from the policies, which a
	// superuser is -- it would archive every other family's questions. It did
	// exactly that once: importing a second family retired all 350 questions
	// belonging to the first.
	tag, err := db.Exec(ctx, `
		UPDATE family.questions
		SET archived_at = now()
		WHERE source = 'import'
		  AND archived_at IS NULL
		  AND family_id = $2
		  AND NOT (import_key = ANY($1::text[]))`, keys, FamilyFrom(ctx))
	if err != nil {
		return 0, fmt.Errorf("archive removed questions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) CountQuestions(ctx context.Context) (int, error) {
	var n int
	err := s.q(ctx).QueryRow(ctx,
		`SELECT count(*) FROM family.questions WHERE archived_at IS NULL`).Scan(&n)
	return n, err
}

func (s *Store) CountQuestionsFor(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.q(ctx).QueryRow(ctx,
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
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.questions
		  (subject_id, asked_of_user_id, topic, body, sort_order, source, created_by_user_id, family_id)
		VALUES ($1, $2, $3, $4,
		        coalesce((SELECT max(sort_order) + 1 FROM family.questions), 0),
		        'user', $5,
		        (SELECT family_id FROM family.subjects WHERE id = $1))
		RETURNING id`, subjectID, askedOfUserID, topic, body, authorID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create question: %w", err)
	}
	// Being asked lives in question_askees, and a question with nobody in it would
	// appear in no card stack at all.
	if err := AddAskee(ctx, s.q(ctx), id, askedOfUserID); err != nil {
		return 0, err
	}
	return id, nil
}

// AddAskee records that a question is put to somebody. Repeating it is harmless,
// which matters because an import runs over the same prompts every time.
//
// The line comes from the question rather than from the context. A web request
// carries the several families somebody belongs to and not one chosen family, so
// reading it from the context inserted a zero and the write failed on the foreign
// key -- which is the right failure, but the wrong question to have asked.
func AddAskee(ctx context.Context, db DBTX, questionID, userID int64) error {
	if _, err := db.Exec(ctx, `
		INSERT INTO family.question_askees (family_id, question_id, user_id)
		SELECT q.family_id, q.id, $2 FROM family.questions q WHERE q.id = $1
		ON CONFLICT DO NOTHING`, questionID, userID); err != nil {
		return fmt.Errorf("add askee: %w", err)
	}
	return nil
}

// Askee is one person a question is put to.
type Askee struct {
	QuestionID int64
	UserID     int64
}

// PruneAskeesNotIn detaches people the import no longer asks, within this family.
//
// family_id is named explicitly rather than left to row-level security, for the
// same reason the archive statement names it: this deletes everything it does not
// recognise, and a statement like that must never depend on a session setting
// being right.
func PruneAskeesNotIn(ctx context.Context, db DBTX, keep []Askee) (int64, error) {
	questionIDs := make([]int64, len(keep))
	userIDs := make([]int64, len(keep))
	for i, a := range keep {
		questionIDs[i] = a.QuestionID
		userIDs[i] = a.UserID
	}
	tag, err := db.Exec(ctx, `
		DELETE FROM family.question_askees a
		 USING family.questions q
		 WHERE q.id = a.question_id
		   AND a.family_id = $3
		   AND q.source = 'import'
		   AND q.archived_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM unnest($1::bigint[], $2::bigint[]) AS k(question_id, user_id)
		        WHERE k.question_id = a.question_id AND k.user_id = a.user_id)`,
		questionIDs, userIDs, FamilyFrom(ctx))
	if err != nil {
		return 0, fmt.Errorf("prune askees: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AskAlso puts an existing question to one more person.
func (s *Store) AskAlso(ctx context.Context, questionID, userID int64) error {
	return AddAskee(ctx, s.q(ctx), questionID, userID)
}
