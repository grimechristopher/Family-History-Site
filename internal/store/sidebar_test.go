package store

import (
	"context"
	"testing"
)

// Chris belongs to four lines, so the sidebar he sees is four sidebars stacked.
// Choosing a line has to narrow it, or the list of people to answer for and the
// list of people to read about stay four times longer than anything he wants.
//
// Both halves are checked here because they narrow independently and a page that
// filtered only one of them would look right until you clicked.
func TestSidebarNarrowsToOneLine(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	type line struct {
		slug, name  string
		id          int64
		contributor string
		subject     string
	}
	lines := []line{
		{slug: "grime", name: "The Grime line", contributor: "James Grime", subject: "James Andrew Grime"},
		{slug: "lucero", name: "The Lucero line", contributor: "Frank Lucero", subject: "Frank Lucero"},
	}

	// A contributor only counts if they have a question waiting, so each line gets
	// one. Everything is created in its own family context, the way the importer
	// does it.
	var memberOf []int64
	for i := range lines {
		l := &lines[i]
		id, err := s.CreateFamily(ctx, l.slug, l.name)
		if err != nil {
			t.Fatalf("CreateFamily %s: %v", l.slug, err)
		}
		l.id = id
		memberOf = append(memberOf, id)

		fctx := WithFamily(ctx, id)
		err = s.InTx(fctx, func(db DBTX) error {
			uid, err := UpsertUser(fctx, db, l.slug+"@example.com", l.contributor)
			if err != nil {
				return err
			}
			if err := AddMemberTx(fctx, db, id, uid, RoleContributor); err != nil {
				return err
			}
			sid, err := UpsertSubject(fctx, db, Subject{
				Slug: l.slug + "-them", Kind: "individual", DisplayName: l.subject, SortOrder: 1,
			})
			if err != nil {
				return err
			}
			_, err = UpsertImportedQuestion(fctx, db, ImportedQuestion{
				SubjectID: sid, AskedOfUserID: uid,
				Body: "Where were you born?", SortOrder: 1, ImportKey: l.slug + "-q1",
			})
			return err
		})
		if err != nil {
			t.Fatalf("seed %s: %v", l.slug, err)
		}
	}

	ctx = WithFamilies(ctx, memberOf)

	t.Run("everything unfiltered", func(t *testing.T) {
		cs, err := s.Contributors(ctx, "")
		if err != nil {
			t.Fatalf("Contributors: %v", err)
		}
		if len(cs) != 2 {
			t.Fatalf("contributors across both lines = %d, want 2: %v", len(cs), names(cs))
		}
		subs, err := s.SubjectsWithProgress(ctx, "", "")
		if err != nil {
			t.Fatalf("SubjectsWithProgress: %v", err)
		}
		if len(subs) != 2 {
			t.Fatalf("subjects across both lines = %d, want 2", len(subs))
		}
	})

	for _, l := range lines {
		t.Run(l.slug, func(t *testing.T) {
			cs, err := s.Contributors(ctx, l.slug)
			if err != nil {
				t.Fatalf("Contributors: %v", err)
			}
			if len(cs) != 1 || cs[0].DisplayName != l.contributor {
				t.Errorf("contributors in %s = %v, want [%s]", l.slug, names(cs), l.contributor)
			}

			subs, err := s.SubjectsWithProgress(ctx, "", l.slug)
			if err != nil {
				t.Fatalf("SubjectsWithProgress: %v", err)
			}
			if len(subs) != 1 || subs[0].DisplayName != l.subject {
				t.Fatalf("subjects in %s = %d rows, want just %s", l.slug, len(subs), l.subject)
			}
			// The line travels with the subject, so a link can name it. Without this
			// the four subjects called "Further Back" are indistinguishable.
			if subs[0].FamilySlug != l.slug || subs[0].FamilyName != l.name {
				t.Errorf("subject carries line %q/%q, want %q/%q",
					subs[0].FamilySlug, subs[0].FamilyName, l.slug, l.name)
			}
		})
	}
}

func names(us []*User) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.DisplayName
	}
	return out
}
