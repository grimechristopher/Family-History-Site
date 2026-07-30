// Package importer writes the parsed GEDCOM and prompts file into Postgres.
//
// Everything runs in one transaction, in dependency order: people, then subjects,
// then users (which reference people), then questions (which reference both
// subjects and users). A failure anywhere leaves the database untouched.
package importer

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/prompts"
	"github.com/grimechristopher/family-history-site/internal/store"
	"github.com/grimechristopher/family-history-site/internal/subjects"
)

// Contributor is a person who will be asked questions. Label must match the "#"
// heading used in the prompts markdown.
type Contributor struct {
	Label string // the "# ..." heading in the prompts file: "Dad", "Frank"
	Email string
	// GedcomName may be empty. A sibling who has not been added to the tree yet is
	// still somebody questions are asked of -- they simply have no person to link
	// to and are not a root of the ancestor walk.
	GedcomName string // "Peter John /Hale/"
}

// Admin is Chris: allowed in, but not asked anything.
type Admin struct {
	Label string
	Email string
}

// GenericQuestions is what every great-grandparent couple is asked.
var GenericQuestions = subjects.GenericCoupleQuestions

type Options struct {
	Tree         subjects.Options
	Contributors []Contributor
	Admins       []Admin
	Overrides    subjects.Overrides
}

// RoutedQuestion is a further-back question moved onto a named couple.
type RoutedQuestion struct {
	Subject string
	Body    string
}

// Result reports what the import did, for printing to the operator.
type Result struct {
	// Routed lists the questions moved out of the further-back bucket, so the
	// decisions are visible rather than silent.
	Routed    []RoutedQuestion
	People    int
	Subjects  int
	Users     int
	Questions int
	Generic   int
	Archived  int64
	Matched   int
	PerPerson map[string]int
}

// Run performs the whole import inside db, which is expected to be a transaction.
func Run(ctx context.Context, db store.DBTX, ged *gedcom.File, qs []prompts.Question, opts Options) (*Result, error) {
	tree, err := subjects.Derive(ged, opts.Tree)
	if err != nil {
		return nil, fmt.Errorf("derive tree: %w", err)
	}

	res := &Result{PerPerson: map[string]int{}}

	// 1. People. Inserted first without parent links, since a parent row may not
	//    exist yet, then linked in a second pass.
	personIDs := make(map[string]int64, len(tree.People))
	gedcomIDs := make([]string, 0, len(tree.People))
	for id := range tree.People {
		gedcomIDs = append(gedcomIDs, id)
	}
	sort.Strings(gedcomIDs)

	for _, gid := range gedcomIDs {
		p := tree.People[gid]
		id, err := store.UpsertPerson(ctx, db, store.Person{
			GedcomID:       p.GedcomID,
			Given:          p.Given,
			Surname:        p.Surname,
			MarriedSurname: optionalString(p.MarriedSurname),
			Sex:            optionalString(p.Sex),
			BirthYear:      optionalInt(p.BirthYear),
			DeathYear:      optionalInt(p.DeathYear),
		})
		if err != nil {
			return nil, err
		}
		personIDs[gid] = id
	}
	res.People = len(personIDs)

	for _, gid := range gedcomIDs {
		p := tree.People[gid]
		var father, mother *int64
		if id, ok := personIDs[p.FatherID]; ok && p.FatherID != "" {
			father = &id
		}
		if id, ok := personIDs[p.MotherID]; ok && p.MotherID != "" {
			mother = &id
		}
		if father == nil && mother == nil {
			continue
		}
		if err := store.SetParents(ctx, db, personIDs[gid], father, mother); err != nil {
			return nil, err
		}
	}

	// 2. Subjects and their membership.
	subjectIDs := make(map[string]int64, len(tree.Subjects))
	for _, s := range tree.Subjects {
		id, err := store.UpsertSubject(ctx, db, store.Subject{
			Slug:        s.Slug,
			Kind:        s.Kind,
			DisplayName: s.DisplayName,
			SortOrder:   s.SortOrder,
		})
		if err != nil {
			return nil, err
		}
		subjectIDs[s.Slug] = id

		members := make([]int64, 0, len(s.MemberIDs))
		for _, gid := range s.MemberIDs {
			if pid, ok := personIDs[gid]; ok {
				members = append(members, pid)
			}
		}
		if err := store.SetSubjectMembers(ctx, db, id, members); err != nil {
			return nil, err
		}
	}
	res.Subjects = len(subjectIDs)

	// 3. Users. Contributors are linked to their own person row so relationship
	//    labels can later be computed per viewer.
	userIDs := map[string]int64{}
	rootLabels := map[string]string{}
	// Contributors with no record in the tree, and the subject made for each.
	offTree := map[string]string{}
	for _, c := range opts.Contributors {
		// Somebody who is not in the tree yet: no person to link, no name to take,
		// but still asked questions.
		if c.GedcomName == "" {
			uid, err := store.UpsertUser(ctx, db, c.Email, c.Label)
			if err != nil {
				return nil, err
			}
			if err := store.AddMemberTx(ctx, db, store.FamilyFrom(ctx), uid, store.RoleContributor); err != nil {
				return nil, err
			}
			userIDs[c.Label] = uid

			// Their own questions -- "About You" -- need a subject to hang from, and
			// the tree has none for them. One is made from their name, so a sibling
			// missing from the tree is still somebody the site can ask about.
			slug := subjects.Slugify(c.Label)
			sid, err := store.UpsertSubject(ctx, db, store.Subject{
				Slug: slug, Kind: "individual", DisplayName: c.Label,
				SortOrder: 900 + len(offTree),
			})
			if err != nil {
				return nil, err
			}
			subjectIDs[slug] = sid
			offTree[c.Label] = slug
			continue
		}

		gid, err := ged.FindByName(c.GedcomName)
		if err != nil {
			return nil, fmt.Errorf("contributor %s: %w", c.Label, err)
		}
		pid, ok := personIDs[gid]
		if !ok {
			return nil, fmt.Errorf("contributor %s (%s) is not inside the imported tree", c.Label, c.GedcomName)
		}

		// Shown under their own name rather than "Dad" or "Mom". Those are only
		// meaningful to whoever wrote the prompts file: with more than one family
		// on the site, every family has a Dad, and an answer signed "Dad" says
		// nothing about whose father it was. The heading in the markdown stays
		// "Dad" -- that is the writer's word for them -- and only the name shown
		// on screen changes.
		display := c.Label
		if p := tree.People[gid]; p != nil {
			display = personDisplayName(p)
		}

		uid, err := store.UpsertUser(ctx, db, c.Email, display)
		if err != nil {
			return nil, err
		}
		if err := store.AddMemberTx(ctx, db, store.FamilyFrom(ctx), uid, store.RoleContributor); err != nil {
			return nil, err
		}
		userIDs[c.Label] = uid
		rootLabels[c.Label] = c.GedcomName

		if err := store.LinkUserToPerson(ctx, db, uid, pid); err != nil {
			return nil, err
		}
	}
	for _, a := range opts.Admins {
		uid, err := store.UpsertUser(ctx, db, a.Email, a.Label)
		if err != nil {
			return nil, err
		}
		if err := store.AddMemberTx(ctx, db, store.FamilyFrom(ctx), uid, store.RoleAdmin); err != nil {
			return nil, err
		}
	}
	res.Users = len(opts.Contributors) + len(opts.Admins)

	// 4. Questions. Headings are matched onto subjects; an unresolved heading
	//    aborts the import rather than filing questions under a guess.
	personSubjects, err := tree.PersonSubjects(rootLabels)
	if err != nil {
		return nil, err
	}
	// Contributors who are not in the tree carry their own subject rather than one
	// derived from it.
	for label, slug := range offTree {
		personSubjects[label] = slug
	}
	headings, _ := prompts.Headings(qs)
	matches, ambiguities := tree.MatchHeadings(headings, personSubjects, opts.Overrides)
	if len(ambiguities) > 0 {
		return nil, &UnresolvedError{Ambiguities: ambiguities}
	}
	res.Matched = len(matches)

	matchByHeading := make(map[prompts.Heading]subjects.Match, len(matches))
	for _, m := range matches {
		matchByHeading[m.Heading] = m
	}

	keys := make([]string, 0, len(qs))
	// Counts the same words asked twice about the same person. Nothing else about
	// position matters: the key is a hash of the question.
	occurrences := map[string]int{}

	for i, q := range qs {
		m, ok := matchByHeading[q.Heading()]
		if !ok {
			return nil, fmt.Errorf("no match for heading %s", q.Heading())
		}
		subject := m.Subject

		// A question that landed in the further-back bucket may still name one
		// couple outright — "the Aldermans were immigrants from Sweden" — in which
		// case it belongs on their page. Only unambiguous ones move; every
		// decision is recorded so it can be reviewed rather than trusted.
		if subject == subjects.FurtherBackSlug {
			if routed := tree.RouteToCouple(q.Body); routed.Subject != "" {
				subject = routed.Subject
				res.Routed = append(res.Routed, RoutedQuestion{
					Subject: routed.Subject,
					Body:    q.Body,
				})
			}
		}

		subjectID, ok := subjectIDs[subject]
		if !ok {
			return nil, fmt.Errorf("heading %s resolved to unknown subject %q", q.Heading(), subject)
		}
		userID, ok := userIDs[q.Person]
		if !ok {
			// A person heading with no contributor row is skipped rather than
			// failing: the prompts file has an empty "# Stephanie" section, and
			// she is deliberately out of scope for now.
			continue
		}

		// m.Subject, not the routed subject: routing a question onto a couple's
		// page must not change what it is, or the move would archive its answers.
		identity := q.Person + "|" + m.Subject + "|" + q.Body
		occurrences[identity]++
		key := prompts.ImportKey(q.Person, m.Subject, q.Body, occurrences[identity])
		if _, err := store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID:     subjectID,
			AskedOfUserID: userID,
			Topic:         optionalString(m.Topic),
			Body:          q.Body,
			SortOrder:     i,
			IsProposed:    q.IsProposed,
			ImportKey:     key,
		}); err != nil {
			return nil, err
		}
		keys = append(keys, key)
		res.Questions++
		res.PerPerson[q.Person]++
	}

	// 5. A handful of generic questions for each great-grandparent couple. The
	//    markdown covers those generations as surname lines rather than as people,
	//    so the couples themselves arrive with nothing to answer.
	//
	//    Keyed in their own namespace, so editing the markdown never renumbers
	//    them and they are archived only if this list itself changes.
	byLineage := tree.CouplesByLineage()
	for _, c := range opts.Contributors {
		// Generic questions are attributed by lineage, and somebody who is not in
		// the tree has no lineage to attribute them to. Their siblings who are in it
		// already cover those couples.
		if c.GedcomName == "" {
			continue
		}
		rootGedcomID, err := ged.FindByName(c.GedcomName)
		if err != nil {
			return nil, err
		}
		for _, sub := range byLineage[rootGedcomID] {
			subjectID, ok := subjectIDs[sub.Slug]
			if !ok {
				continue
			}
			for i, body := range GenericQuestions {
				key := prompts.ImportKey(c.Label, sub.Slug, body, 1)
				if _, err := store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
					SubjectID:     subjectID,
					AskedOfUserID: userIDs[c.Label],
					Body:          body,
					SortOrder:     10000 + i, // after everything from the markdown
					ImportKey:     key,
				}); err != nil {
					return nil, err
				}
				keys = append(keys, key)
				res.Questions++
				res.Generic++
				res.PerPerson[c.Label]++
			}
		}
	}

	archived, err := store.ArchiveImportedQuestionsNotIn(ctx, db, keys)
	if err != nil {
		return nil, err
	}
	res.Archived = archived

	return res, nil
}

// UnresolvedError reports headings the matcher would not guess at.
type UnresolvedError struct {
	Ambiguities []subjects.Ambiguity
}

func (e *UnresolvedError) Error() string {
	s := fmt.Sprintf("%d heading(s) could not be resolved to a subject:", len(e.Ambiguities))
	for _, a := range e.Ambiguities {
		s += "\n  " + a.Error()
	}
	s += "\n\nAdd an override for each, or name a missing individual in ExtraNames."
	return s
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalInt(i int) *int {
	if i == 0 {
		return nil
	}
	return &i
}

// personDisplayName is how somebody is named on screen: their first given name and
// the surname they are known by. "James Andrew" and "Grime" become "James Grime",
// and a woman recorded under her maiden name is shown under her married one, which
// is what her family calls her.
//
// Short on purpose. It appears in "Hello ...", against every answer, and in the
// list of who a question was asked of, so the full four-part name would crowd all
// three.
func personDisplayName(p *subjects.Person) string {
	given := p.Given
	if i := strings.IndexByte(given, ' '); i > 0 {
		given = given[:i]
	}
	surname := p.Surname
	if p.MarriedSurname != "" {
		surname = p.MarriedSurname
	}
	return strings.TrimSpace(given + " " + surname)
}
