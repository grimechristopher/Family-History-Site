package store

import (
	"context"
	"testing"
)

// Robert, Frank, Tony and Inez are asked the same questions about their parents.
// Each writes his or her own answer -- a card stack that skipped Tony because
// Frank had already answered would lose three quarters of the point -- so the rows
// stay separate. What must not happen is the list showing the same question four
// times, or the heading counting four.
func TestOneQuestionAskedOfSeveralPeople(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "lucero", "The Lucero line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(ctx, famID)

	var subjectID int64
	var viewerID int64
	brothers := []string{"Robert Lucero", "Frank Lucero", "Tony Lucero"}
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		subjectID, err = UpsertSubject(fctx, db, Subject{
			Slug: "louis", Kind: "individual", DisplayName: "Louis J Lucero", SortOrder: 1,
		})
		if err != nil {
			return err
		}
		for i, name := range brothers {
			uid, err := UpsertUser(fctx, db, "b"+name[:1]+"@example.com", name)
			if err != nil {
				return err
			}
			if err := AddMemberTx(fctx, db, famID, uid, RoleContributor); err != nil {
				return err
			}
			if i == 0 {
				viewerID = uid
			}
			// The same question -- same import key, because the key is content and
			// carries no person -- and one of his own.
			if _, err := UpsertImportedQuestion(fctx, db, ImportedQuestion{
				SubjectID: subjectID, AskedOfUserID: uid,
				Body: "What do you remember most about him?", SortOrder: 1,
				ImportKey: "shared",
			}); err != nil {
				return err
			}
			if _, err := UpsertImportedQuestion(fctx, db, ImportedQuestion{
				SubjectID: subjectID, AskedOfUserID: uid,
				Body: "A question only for " + name, SortOrder: 2,
				ImportKey: name + "-own",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx = WithFamilies(fctx, []int64{famID})

	t.Run("everybody at once", func(t *testing.T) {
		items, err := s.ListQuestions(ctx, viewerID, QuestionFilter{})
		if err != nil {
			t.Fatalf("ListQuestions: %v", err)
		}
		// Six rows in the table; four questions to a reader.
		if len(items) != 4 {
			bodies := make([]string, len(items))
			for i, q := range items {
				bodies[i] = q.Body + " (" + q.AskedOfName + ")"
			}
			t.Fatalf("listed %d questions, want 4: %v", len(items), bodies)
		}

		var shared *QuestionListItem
		for i := range items {
			if len(items[i].SharedWith) > 0 {
				shared = &items[i]
			}
		}
		if shared == nil {
			t.Fatal("the question asked of all three is not marked as shared")
		}
		if len(shared.SharedWith) != 3 {
			t.Errorf("shared with %v, want all three brothers", shared.SharedWith)
		}
		if got := shared.SharedWithSentence(); got != "Frank Lucero, Robert Lucero and Tony Lucero" {
			t.Errorf("named them as %q", got)
		}

		counts, err := s.ListCounts(ctx, QuestionFilter{})
		if err != nil {
			t.Fatalf("ListCounts: %v", err)
		}
		if counts.Unanswered != 4 {
			t.Errorf("heading says %d waiting above 4 rows", counts.Unanswered)
		}
	})

	// Filtered to one person it is his question, and collapsing would hide it.
	t.Run("one person", func(t *testing.T) {
		items, err := s.ListQuestions(ctx, viewerID, QuestionFilter{AskedOfName: "Frank Lucero"})
		if err != nil {
			t.Fatalf("ListQuestions: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("Frank has %d questions, want his own two", len(items))
		}
		counts, err := s.ListCounts(ctx, QuestionFilter{AskedOfName: "Frank Lucero"})
		if err != nil {
			t.Fatalf("ListCounts: %v", err)
		}
		if counts.Unanswered != 2 {
			t.Errorf("Frank's heading says %d waiting above 2 rows", counts.Unanswered)
		}
	})
}
