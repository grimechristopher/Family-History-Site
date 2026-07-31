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

	// SharedWith names everybody this same question is asked of, when that is more
	// than one person. Robert, Frank, Tony and Inez are asked the same ten
	// questions about their parents; each still writes his or her own answer, but
	// the list has no business showing the question four times.
	SharedWith []string
	// SharedAnswered is how many of them have answered.
	SharedAnswered int
	// HideAskedOf is set when the whole list is one person's, where naming them on
	// every row is their own name repeated back at them a hundred and seventy-two
	// times. A question put to several people still names them all: there it is
	// information rather than noise.
	HideAskedOf bool

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
	// $1 is the viewer, $2 the person the page is filtered to. The second is always
	// bound, empty or not, so the join below has a fixed position to refer to.
	args := []any{viewerID, f.AskedOfName}

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
		add(`EXISTS (SELECT 1 FROM family.question_askees qa
		              JOIN core.users qau ON qau.id = qa.user_id
		             WHERE qa.question_id = q.id AND qau.display_name = $%d)`, f.AskedOfName)
	}
	// "Answered" depends on who is reading. Filtered to one person it is whether
	// that person has answered; across everybody it is whether anybody asked has.
	answeredBy := "owner_answer.id IS NOT NULL"
	if f.AskedOfName != "" {
		answeredBy = "chosen_answer.id IS NOT NULL"
	}
	if f.OnlyUnanswered {
		where = append(where, "NOT ("+answeredBy+")")
	}
	if f.OnlyAnswered {
		where = append(where, answeredBy)
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
		              AND fm.removed_at IS NULL
		            WHERE sm.subject_id = q.subject_id
		              AND fm.user_id = q.asked_of_user_id
		       ) AS about_asked_of,
		       (owner_answer.id IS NOT NULL) AS answered,
		       coalesce(counts.other_answers, 0),
		       coalesce(counts.replies, 0),
		       (viewer_answer.id IS NOT NULL) AS viewer_answered,
		       -- Everybody this same question is asked of, and how many have
		       -- answered. A subquery rather than a window, so it describes the
		       -- question rather than whichever of its rows survived the filter:
		       -- under "still waiting" a window would report that a question was
		       -- asked only of the people who have not answered it.
		       shared.names, coalesce(shared.answered, 0)
		FROM family.questions q
		JOIN family.subjects s   ON s.id = q.subject_id
		JOIN core.users asked  ON asked.id = q.asked_of_user_id
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.is_draft = false
		      AND EXISTS (SELECT 1 FROM family.question_askees oa
		                   WHERE oa.question_id = q.id
		                     AND oa.user_id = owner_answer.author_user_id)
		LEFT JOIN family.entries chosen_answer
		       ON chosen_answer.question_id = q.id
		      AND chosen_answer.is_draft = false
		      AND chosen_answer.author_user_id =
		          (SELECT id FROM core.users WHERE display_name = $2 LIMIT 1)
		LEFT JOIN family.entries viewer_answer
		       ON viewer_answer.question_id = q.id
		      AND viewer_answer.author_user_id = $1
		      AND viewer_answer.is_draft = false
		LEFT JOIN (
		    SELECT e.question_id,
		           count(*) FILTER (
		               WHERE NOT EXISTS (SELECT 1 FROM family.question_askees oa
		                                  WHERE oa.question_id = e.question_id
		                                    AND oa.user_id = e.author_user_id)
		           ) AS other_answers,
		           coalesce(sum(r.n), 0) AS replies
		    FROM family.entries e
		    JOIN family.questions q2 ON q2.id = e.question_id
		    LEFT JOIN (
		        SELECT entry_id, count(*) AS n FROM family.replies GROUP BY entry_id
		    ) r ON r.entry_id = e.id
		    WHERE e.is_draft = false
		    GROUP BY e.question_id
		) counts ON counts.question_id = q.id
		LEFT JOIN LATERAL (
		    SELECT array_agg(sib.display_name ORDER BY sib.display_name) AS names,
		           count(sib_answer.id) AS answered
		    FROM family.question_askees sa
		    JOIN core.users sib ON sib.id = sa.user_id
		    LEFT JOIN family.entries sib_answer
		           ON sib_answer.question_id = sa.question_id
		          AND sib_answer.author_user_id = sa.user_id
		          AND sib_answer.is_draft = false
		    WHERE sa.question_id = q.id
		) shared ON true
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
			&q.Answered, &q.OtherAnswers, &q.ReplyCount, &q.ViewerAnswered,
			&q.SharedWith, &q.SharedAnswered); err != nil {
			return nil, err
		}
		if len(q.SharedWith) < 2 {
			// One person asked: nothing to say about who else.
			q.SharedWith = nil
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
		where = append(where, fmt.Sprintf(`EXISTS (
		    SELECT 1 FROM family.question_askees qa
		      JOIN core.users qau ON qau.id = qa.user_id
		     WHERE qa.question_id = q.id AND qau.display_name = $%d)`, len(args)))
	}

	// Counted per question rather than per row when nobody is chosen, to agree
	// with the list underneath: the Lucero line has 104 rows and 74 questions, and
	// a heading saying 104 above 74 rows is just wrong.
	// One row per question now, so nothing needs collapsing. The count is of
	// questions either way; what changes with a person chosen is whose answer
	// decides "answered".
	counted := "count(*)"
	answered := "owner_answer.id IS NOT NULL"
	if f.AskedOfName != "" {
		args = append(args, f.AskedOfName)
		answered = fmt.Sprintf(`EXISTS (
		    SELECT 1 FROM family.entries ce JOIN core.users cu ON cu.id = ce.author_user_id
		     WHERE ce.question_id = q.id AND ce.is_draft = false AND cu.display_name = $%d)`, len(args))
	}

	var c ListCounts
	err := s.q(ctx).QueryRow(ctx, `
		SELECT `+counted+` FILTER (WHERE NOT (`+answered+`)),
		       `+counted+` FILTER (WHERE `+answered+`)
		FROM family.questions q
		JOIN family.subjects s  ON s.id = q.subject_id
		JOIN core.users asked ON asked.id = q.asked_of_user_id
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.is_draft = false
		      AND EXISTS (SELECT 1 FROM family.question_askees oa
		                   WHERE oa.question_id = q.id
		                     AND oa.user_id = owner_answer.author_user_id)
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
	// AnyTotal and AnyAnswered count every live question about them and every
	// answer to one, whoever was asked. These are what the rail shows.
	//
	// "Who it's about" is a map of the family, so it says the same thing whoever
	// you happen to be reading. Narrowed to one person it showed Inez three of the
	// eleven people her line has questions about, and hid four great-grandparent
	// couples because those questions had been put to her brother -- which is not
	// what "who it's about" means, and made half the family unreachable from the
	// page built for reaching them.
	AnyTotal    int
	AnyAnswered int
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
		         WHERE aq.subject_id = s.id AND aq.archived_at IS NULL),
		       (SELECT count(*) FROM family.questions aq
		         JOIN family.entries ae ON ae.question_id = aq.id AND ae.is_draft = false
		        WHERE aq.subject_id = s.id AND aq.archived_at IS NULL
		          AND EXISTS (SELECT 1 FROM family.question_askees aa
		                       WHERE aa.question_id = aq.id
		                         AND aa.user_id = ae.author_user_id))
		FROM family.subjects s
		JOIN core.families f ON f.id = s.family_id
		LEFT JOIN core.users asked
		       ON $1 <> '' AND asked.display_name = $1
		LEFT JOIN family.questions q
		       ON q.subject_id = s.id
		      AND q.archived_at IS NULL
		      AND ($1 = '' OR EXISTS (SELECT 1 FROM family.question_askees qa
		                               WHERE qa.question_id = q.id
		                                 AND qa.user_id = asked.id))
		LEFT JOIN family.entries owner_answer
		       ON owner_answer.question_id = q.id
		      AND owner_answer.is_draft = false
		      AND EXISTS (SELECT 1 FROM family.question_askees oa
		                   WHERE oa.question_id = q.id
		                     AND oa.user_id = owner_answer.author_user_id)
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
			&p.Total, &p.Answered, &p.Stories, &p.AnyTotal, &p.AnyAnswered); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SharedWithSentence names everybody a question is asked of, the way a person
// would say it: "Robert, Frank, Tony and Inez".
func (q QuestionListItem) SharedWithSentence() string {
	switch n := len(q.SharedWith); n {
	case 0:
		return q.AskedOfName
	case 1:
		return q.SharedWith[0]
	case 2:
		return q.SharedWith[0] + " and " + q.SharedWith[1]
	default:
		return strings.Join(q.SharedWith[:n-1], ", ") + " and " + q.SharedWith[n-1]
	}
}

// LineStanding is how one line is getting on, for the page an admin lands on.
type LineStanding struct {
	Slug        string
	DisplayName string
	Answered    int
	Total       int
	// Recent counts answers written in the last week, which is the only number
	// that says whether this is alive or has stalled.
	Recent int
}

// Standings summarises every line the viewer belongs to.
//
// The admins are not asked anything, so a page built around their own progress
// told them nothing -- and, worse, congratulated them for having answered
// everything when nothing had ever been asked of them. This is what they actually
// want to know: who is writing, and who has not started.
func (s *Store) Standings(ctx context.Context) ([]LineStanding, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT f.slug, f.display_name,
		       count(*) FILTER (WHERE answer.id IS NOT NULL),
		       count(*),
		       count(*) FILTER (WHERE answer.created_at > now() - interval '7 days')
		  FROM family.questions q
		  JOIN core.families f ON f.id = q.family_id
		  LEFT JOIN family.entries answer
		         ON answer.question_id = q.id
		        AND answer.is_draft = false
		        AND EXISTS (SELECT 1 FROM family.question_askees a
		                     WHERE a.question_id = q.id AND a.user_id = answer.author_user_id)
		 WHERE q.archived_at IS NULL AND q.family_id = ANY($1)
		 GROUP BY f.slug, f.display_name
		 ORDER BY f.display_name`, FamilyIDsFrom(ctx))
	if err != nil {
		return nil, fmt.Errorf("standings: %w", err)
	}
	defer rows.Close()

	var out []LineStanding
	for rows.Next() {
		var l LineStanding
		if err := rows.Scan(&l.Slug, &l.DisplayName, &l.Answered, &l.Total, &l.Recent); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PersonStanding is one contributor's progress, for the same page.
type PersonStanding struct {
	DisplayName string
	Answered    int
	Total       int
	Recent      int
}

// PeopleStandings is everybody who has questions waiting, and how far they have
// got. Sorted by who has done most recently, so the page opens on what is alive.
func (s *Store) PeopleStandings(ctx context.Context) ([]PersonStanding, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT u.display_name,
		       count(*) FILTER (WHERE answer.id IS NOT NULL),
		       count(*),
		       count(*) FILTER (WHERE answer.created_at > now() - interval '7 days')
		  FROM family.question_askees a
		  JOIN family.questions q ON q.id = a.question_id
		  JOIN core.users u ON u.id = a.user_id
		  LEFT JOIN family.entries answer
		         ON answer.question_id = q.id
		        AND answer.author_user_id = a.user_id
		        AND answer.is_draft = false
		 WHERE q.archived_at IS NULL AND q.family_id = ANY($1)
		 GROUP BY u.display_name
		 ORDER BY 4 DESC, 2 DESC, u.display_name`, FamilyIDsFrom(ctx))
	if err != nil {
		return nil, fmt.Errorf("people standings: %w", err)
	}
	defer rows.Close()

	var out []PersonStanding
	for rows.Next() {
		var p PersonStanding
		if err := rows.Scan(&p.DisplayName, &p.Answered, &p.Total, &p.Recent); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
