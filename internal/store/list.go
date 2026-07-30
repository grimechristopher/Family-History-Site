package store

import (
	"context"
	"fmt"
	"strings"
)

// QuestionListItem is one row in the browsable list.
type QuestionListItem struct {
	ID            int64
	Body          string
	Topic         *string
	SubjectName   string
	SubjectSlug   string
	AskedOfName   string
	AskedOfUserID int64
	IsProposed    bool

	// Answered means the person the question was asked of has answered it. This
	// is what "unanswered" means throughout the site.
	Answered bool
	// OtherAnswers counts published answers from everybody else.
	OtherAnswers int
	ReplyCount   int
	// IViewerAnswered reports whether the person browsing has answered it.
	ViewerAnswered bool
}

// QuestionFilter narrows the list. Zero values mean "everything".
type QuestionFilter struct {
	SubjectSlug string
	AskedOfName string
	// OnlyUnanswered restricts to questions the intended person has not answered.
	OnlyUnanswered bool
	// OnlyAnswered is the complement, for the "answered" section.
	OnlyAnswered bool
	Limit        int
	Offset       int
}

// ListQuestions returns questions ordered unanswered-first, then by the
// markdown's own sequence — the shape asked for at the outset: a list of
// questions, with the unanswered ones up top.
//
// Everyone may read everything, so this is not scoped to the viewer. viewerID
// only decides the ViewerAnswered flag.
func (s *Store) ListQuestions(ctx context.Context, viewerID int64, f QuestionFilter) ([]QuestionListItem, error) {
	var where []string
	args := []any{viewerID}

	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	where = append(where, "q.archived_at IS NULL")
	if f.SubjectSlug != "" {
		add("s.slug = $%d", f.SubjectSlug)
	}
	if f.AskedOfName != "" {
		add("asked.display_name = $%d", f.AskedOfName)
	}
	if f.OnlyUnanswered {
		where = append(where, "owner_answer.id IS NULL")
	}
	if f.OnlyAnswered {
		where = append(where, "owner_answer.id IS NOT NULL")
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	args = append(args, limit)
	limitPos := len(args)
	args = append(args, f.Offset)
	offsetPos := len(args)

	query := `
		SELECT q.id, q.body, q.topic, s.display_name, s.slug,
		       asked.display_name, q.asked_of_user_id, q.is_proposed,
		       (owner_answer.id IS NOT NULL) AS answered,
		       coalesce(counts.other_answers, 0),
		       coalesce(counts.replies, 0),
		       (viewer_answer.id IS NOT NULL) AS viewer_answered
		FROM family.questions q
		JOIN family.subjects s   ON s.id = q.subject_id
		JOIN core.users asked  ON asked.id = q.asked_of_user_id
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.author_user_id = q.asked_of_user_id
		      AND owner_answer.is_draft = false
		LEFT JOIN family.entries viewer_answer
		       ON viewer_answer.question_id = q.id
		      AND viewer_answer.author_user_id = $1
		      AND viewer_answer.is_draft = false
		LEFT JOIN (
		    SELECT e.question_id,
		           count(*) FILTER (WHERE e.author_user_id <> q2.asked_of_user_id) AS other_answers,
		           coalesce(sum(r.n), 0) AS replies
		    FROM family.entries e
		    JOIN family.questions q2 ON q2.id = e.question_id
		    LEFT JOIN (
		        SELECT entry_id, count(*) AS n FROM family.replies GROUP BY entry_id
		    ) r ON r.entry_id = e.id
		    WHERE e.is_draft = false
		    GROUP BY e.question_id
		) counts ON counts.question_id = q.id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY answered ASC, q.sort_order, q.id
		LIMIT $` + fmt.Sprint(limitPos) + ` OFFSET $` + fmt.Sprint(offsetPos)

	rows, err := s.q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	defer rows.Close()

	var out []QuestionListItem
	for rows.Next() {
		var q QuestionListItem
		if err := rows.Scan(&q.ID, &q.Body, &q.Topic, &q.SubjectName, &q.SubjectSlug,
			&q.AskedOfName, &q.AskedOfUserID, &q.IsProposed,
			&q.Answered, &q.OtherAnswers, &q.ReplyCount, &q.ViewerAnswered); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// QuestionGroup is a run of questions under one heading — a period of somebody's
// life ("Childhood", "Work & Career") or one relative.
type QuestionGroup struct {
	Label string
	Items []QuestionListItem
}

// GroupQuestions gathers a list into headed groups, keeping the order it arrived
// in. That order comes from the markdown, which runs roughly chronologically —
// childhood, school, college, work, being a parent, looking back — so grouping
// this way needs no separate notion of time.
func GroupQuestions(items []QuestionListItem) []QuestionGroup {
	var groups []QuestionGroup
	index := map[string]int{}

	for _, q := range items {
		label := q.SubjectName
		if q.Topic != nil && *q.Topic != "" {
			label = *q.Topic
		}
		if at, seen := index[label]; seen {
			groups[at].Items = append(groups[at].Items, q)
			continue
		}
		index[label] = len(groups)
		groups = append(groups, QuestionGroup{Label: label, Items: []QuestionListItem{q}})
	}
	return groups
}

// ListCounts drives the section headings on the list page.
type ListCounts struct {
	Unanswered int
	Answered   int
}

func (s *Store) ListCounts(ctx context.Context, f QuestionFilter) (ListCounts, error) {
	var where []string
	var args []any

	where = append(where, "q.archived_at IS NULL")
	if f.SubjectSlug != "" {
		args = append(args, f.SubjectSlug)
		where = append(where, fmt.Sprintf("s.slug = $%d", len(args)))
	}
	if f.AskedOfName != "" {
		args = append(args, f.AskedOfName)
		where = append(where, fmt.Sprintf("asked.display_name = $%d", len(args)))
	}

	var c ListCounts
	err := s.q(ctx).QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE owner_answer.id IS NULL),
		       count(*) FILTER (WHERE owner_answer.id IS NOT NULL)
		FROM family.questions q
		JOIN family.subjects s  ON s.id = q.subject_id
		JOIN core.users asked ON asked.id = q.asked_of_user_id
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.author_user_id = q.asked_of_user_id
		      AND owner_answer.is_draft = false
		WHERE `+strings.Join(where, " AND "), args...).Scan(&c.Unanswered, &c.Answered)
	if err != nil {
		return ListCounts{}, fmt.Errorf("list counts: %w", err)
	}
	return c, nil
}

// SubjectProgress is a subject with how much has been said about them.
type SubjectProgress struct {
	Subject
	Total    int
	Answered int
}

// SubjectsWithProgress lists subjects and how much has been said about them.
//
// askedOf narrows the counts to one contributor, so filtering to Dad shows only
// the people Dad has questions about — offering a person with nothing to answer
// is a dead end.
func (s *Store) SubjectsWithProgress(ctx context.Context, askedOf string) ([]SubjectProgress, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT s.id, s.slug, s.kind, s.display_name, s.sort_order,
		       count(q.id),
		       count(owner_answer.id)
		FROM family.subjects s
		LEFT JOIN core.users asked
		       ON $1 <> '' AND asked.display_name = $1
		LEFT JOIN family.questions q
		       ON q.subject_id = s.id
		      AND q.archived_at IS NULL
		      AND ($1 = '' OR q.asked_of_user_id = asked.id)
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.author_user_id = q.asked_of_user_id
		      AND owner_answer.is_draft = false
		GROUP BY s.id, s.slug, s.kind, s.display_name, s.sort_order
		ORDER BY s.sort_order, s.slug`, askedOf)
	if err != nil {
		return nil, fmt.Errorf("subjects with progress: %w", err)
	}
	defer rows.Close()

	var out []SubjectProgress
	for rows.Next() {
		var p SubjectProgress
		if err := rows.Scan(&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.SortOrder,
			&p.Total, &p.Answered); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
