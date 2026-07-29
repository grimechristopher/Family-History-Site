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

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/prompts"
	"github.com/grimechristopher/family-history-site/internal/store"
	"github.com/grimechristopher/family-history-site/internal/subjects"
)

// Contributor is a person who will be asked questions. Label must match the "#"
// heading used in the prompts markdown.
type Contributor struct {
	Label      string // "Dad", "Mom"
	Email      string
	GedcomName string // "Peter John /Hale/"
}

// Admin is Chris: allowed in, but not asked anything.
type Admin struct {
	Label string
	Email string
}

type Options struct {
	Tree         subjects.Options
	Contributors []Contributor
	Admins       []Admin
	Overrides    subjects.Overrides
}

// Result reports what the import did, for printing to the operator.
type Result struct {
	People    int
	Subjects  int
	Users     int
	Questions int
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
	for _, c := range opts.Contributors {
		uid, err := store.UpsertUser(ctx, db, c.Email, c.Label, store.RoleContributor)
		if err != nil {
			return nil, err
		}
		userIDs[c.Label] = uid
		rootLabels[c.Label] = c.GedcomName

		gid, err := ged.FindByName(c.GedcomName)
		if err != nil {
			return nil, fmt.Errorf("contributor %s: %w", c.Label, err)
		}
		pid, ok := personIDs[gid]
		if !ok {
			return nil, fmt.Errorf("contributor %s (%s) is not inside the imported tree", c.Label, c.GedcomName)
		}
		if err := store.LinkUserToPerson(ctx, db, uid, pid); err != nil {
			return nil, err
		}
	}
	for _, a := range opts.Admins {
		if _, err := store.UpsertUser(ctx, db, a.Email, a.Label, store.RoleAdmin); err != nil {
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
	// Ordinals count within a subject and topic rather than within a heading, so
	// that renaming a heading leaves every question's identity untouched.
	ordinals := map[string]int{}

	for i, q := range qs {
		m, ok := matchByHeading[q.Heading()]
		if !ok {
			return nil, fmt.Errorf("no match for heading %s", q.Heading())
		}
		subjectID, ok := subjectIDs[m.Subject]
		if !ok {
			return nil, fmt.Errorf("heading %s resolved to unknown subject %q", q.Heading(), m.Subject)
		}
		userID, ok := userIDs[q.Person]
		if !ok {
			// A person heading with no contributor row is skipped rather than
			// failing: the prompts file has an empty "# Stephanie" section, and
			// she is deliberately out of scope for now.
			continue
		}

		group := q.Person + "|" + m.Subject + "|" + m.Topic
		ordinals[group]++
		key := prompts.ImportKey(q.Person, m.Subject, m.Topic, ordinals[group])
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
