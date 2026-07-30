package store

import (
	"context"
	"fmt"
	"strings"
)

// QuestionListItem is one row in the browsable list.
type QuestionListItem struct {
	ID          int64
	Body        string
	Topic       *string
	SubjectName string
	// AboutAskedOf marks a question about the person being asked it.
	AboutAskedOf  bool
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
	// FamilySlug narrows to one line. Empty means every family the viewer belongs
	// to, which is what row-level security already limits them to -- this filter
	// only ever narrows within that, so it cannot widen anybody's access.
	FamilySlug string
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
	if f.FamilySlug != "" {
		add("q.family_id = (SELECT id FROM core.families WHERE slug = $%d)", f.FamilySlug)
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
		       -- Whether the question is about the very person being asked it, which
		       -- reads as a mistake if both names are printed: "Lori Ann (Ayres)
		       -- Grime, asked of Lori Grime". Decided by who they are in the tree
		       -- rather than by comparing the two names, because the same person is
		       -- written differently in each -- the subject in full genealogical form,
		       -- the account by the name they go by.
		       EXISTS (
		           SELECT 1 FROM family.subject_members sm
		             JOIN core.family_members fm
		               ON fm.person_id = sm.person_id AND fm.family_id = q.family_id
		            WHERE sm.subject_id = q.subject_id
		              AND fm.user_id = q.asked_of_user_id
		       ) AS about_asked_of,
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
			&q.AskedOfName, &q.AskedOfUserID, &q.IsProposed, &q.AboutAskedOf,
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
	if f.FamilySlug != "" {
		args = append(args, f.FamilySlug)
		where = append(where,
			fmt.Sprintf("q.family_id = (SELECT id FROM core.families WHERE slug = $%d)", len(args)))
	}
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

// SubjectProgress is a subject with how much has been said about them, and which
// line they belong to.
//
// The line matters because somebody in four of them sees four people called
// "Further Back", and the name alone cannot tell them apart.
type SubjectProgress struct {
	Subject
	FamilySlug string
	FamilyName string
	Total      int
	Answered   int
	// Stories written about them, drafts included. A draft counts because
	// somebody has started writing: dropping them out of the list at that moment
	// takes away the page being written on.
	Stories int
	// AnyTotal counts every live question about them, whoever is asked. Total is
	// narrowed to the person being filtered on and is what the page shows; this is
	// what decides whether they are worth listing at all. The two differ for
	// somebody who has questions, just not for the person you are looking at.
	AnyTotal int
}

// SubjectsWithProgress lists subjects and how much has been said about them.
//
// askedOf narrows the counts to one contributor, so filtering to Dad shows only
// the people Dad has questions about — offering a person with nothing to answer
// is a dead end. familySlug narrows to one line; empty means every line this
// person belongs to.
func (s *Store) SubjectsWithProgress(ctx context.Context, askedOf, familySlug string) ([]SubjectProgress, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT s.id, s.slug, s.kind, s.display_name, s.sort_order, s.generation, s.relation,
		       f.slug, f.display_name,
		       count(q.id),
		       count(owner_answer.id),
		       (SELECT count(*) FROM family.entries e WHERE e.subject_id = s.id),
		       (SELECT count(*) FROM family.questions aq
		         WHERE aq.subject_id = s.id AND aq.archived_at IS NULL)
		FROM family.subjects s
		JOIN core.families f ON f.id = s.family_id
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
		WHERE $2 = '' OR f.slug = $2
		GROUP BY s.id, s.slug, s.kind, s.display_name, s.sort_order, s.generation, s.relation,
		         f.slug, f.display_name
		ORDER BY f.slug, s.sort_order, s.slug`, askedOf, familySlug)
	if err != nil {
		return nil, fmt.Errorf("subjects with progress: %w", err)
	}
	defer rows.Close()

	var out []SubjectProgress
	for rows.Next() {
		var p SubjectProgress
		if err := rows.Scan(&p.ID, &p.Slug, &p.Kind, &p.DisplayName, &p.SortOrder,
			&p.Generation, &p.Relation, &p.FamilySlug, &p.FamilyName,
			&p.Total, &p.Answered, &p.Stories, &p.AnyTotal); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
