package store

import (
	"context"
	"fmt"
)

// Card is one question as presented in the stack.
type Card struct {
	QuestionID  int64
	Body        string
	SubjectName string
	SubjectSlug string
	Topic       *string
	IsProposed  bool
	DeferCount  int
	DraftBody   string // any autosaved draft, so typing is never lost
}

// NextCards returns the next questions for a user.
//
// The whole ordering is one sort, with no background job:
//
//	deferred_at ASC NULLS FIRST   never-seen questions lead, then deferrals
//	                              oldest-first, so a swiped card lands behind
//	                              everything and sinks further each swipe
//	shuffle term                  stable per user, so refreshing mid-stack does
//	                              not reshuffle the deck under them
//	sort_order                    the markdown's own sequence
//
// Only published answers remove a question from the queue. A draft does not: the
// question is not finished with until it has been saved for real.
func (s *Store) NextCards(ctx context.Context, u *User, limit int) ([]Card, error) {
	var focus *int64
	if u.QueueMode == QueueOneSubject {
		focus = u.QueueFocusSubjectID
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT q.id, q.body, s.display_name, s.slug, q.topic, q.is_proposed,
		       coalesce(d.defer_count, 0),
		       coalesce(draft.body, '')
		FROM family.questions q
		JOIN family.subjects s ON s.id = q.subject_id
		LEFT JOIN family.entries published
		       ON published.question_id = q.id
		      AND published.author_user_id = $1
		      AND published.is_draft = false
		LEFT JOIN family.entries draft
		       ON draft.question_id = q.id
		      AND draft.author_user_id = $1
		      AND draft.is_draft = true
		LEFT JOIN family.question_deferrals d
		       ON d.question_id = q.id
		      AND d.user_id = $1
		WHERE q.asked_of_user_id = $1
		  AND q.archived_at IS NULL
		  AND published.id IS NULL
		  AND ($2::bigint IS NULL OR q.subject_id = $2)
		ORDER BY d.deferred_at ASC NULLS FIRST,
		         CASE WHEN $3::boolean THEN md5(q.id::text || $4::text) END,
		         q.sort_order, q.id
		LIMIT $5`,
		u.ID, focus, u.QueueMode == QueueShuffle, fmt.Sprint(u.QueueSeed), limit)
	if err != nil {
		return nil, fmt.Errorf("next cards: %w", err)
	}
	defer rows.Close()

	var out []Card
	for rows.Next() {
		var c Card
		if err := rows.Scan(&c.QuestionID, &c.Body, &c.SubjectName, &c.SubjectSlug,
			&c.Topic, &c.IsProposed, &c.DeferCount, &c.DraftBody); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeferQuestion records a swipe. There is no "declined" state: every question
// returns to the queue however many times it is swiped away, by explicit
// decision.
func (s *Store) DeferQuestion(ctx context.Context, questionID, userID int64) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO family.question_deferrals (question_id, user_id, deferred_at, defer_count)
		VALUES ($1, $2, now(), 1)
		ON CONFLICT (question_id, user_id) DO UPDATE SET
		  deferred_at = now(),
		  defer_count = family.question_deferrals.defer_count + 1`,
		questionID, userID)
	if err != nil {
		return fmt.Errorf("defer question %d: %w", questionID, err)
	}
	return nil
}

// Progress is framed as accumulation when displayed: "you've answered 47", never
// "111 remaining".
type Progress struct {
	Answered int
	Total    int
}

func (s *Store) Progress(ctx context.Context, userID int64) (Progress, error) {
	var p Progress
	err := s.Pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM family.entries e
		    JOIN family.questions q ON q.id = e.question_id
		   WHERE e.author_user_id = $1 AND e.is_draft = false AND q.archived_at IS NULL),
		  (SELECT count(*) FROM family.questions
		   WHERE asked_of_user_id = $1 AND archived_at IS NULL)`,
		userID).Scan(&p.Answered, &p.Total)
	if err != nil {
		return Progress{}, fmt.Errorf("progress for user %d: %w", userID, err)
	}
	return p, nil
}

// QuestionOwner reports who a question was asked of, so a handler can reject a
// defer or answer aimed at somebody else's queue.
func (s *Store) QuestionOwner(ctx context.Context, questionID int64) (int64, error) {
	var userID int64
	err := s.Pool.QueryRow(ctx,
		`SELECT asked_of_user_id FROM family.questions WHERE id = $1 AND archived_at IS NULL`,
		questionID).Scan(&userID)
	if err != nil {
		return 0, ErrNotFound
	}
	return userID, nil
}
