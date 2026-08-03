package store

import (
	"context"
	"errors"
	"testing"
)

// The importer is the hazard here, not the handler.
//
// UpsertImportedQuestion sets body from the prompts file and clears archived_at,
// because that is how a corrected wording in the file reaches the site. So a question
// reworded by hand would snap back to the file's version, and one taken off the site
// would reappear, the next time anybody ran scripts/import.sh -- days later, with
// nothing in the request logs to connect it to.
//
// Both actions are tested against a re-import rather than against a read, because a
// read passes whether or not the guard exists.
func TestEditingAndRemovingSurviveAReimport(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	familyID, err := s.CreateFamily(ctx, "guard", "The Guard line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{familyID}), familyID)

	var reworded, removed, untouched int64
	var admin int64
	// The same import call the script makes, so the test exercises the real path.
	reimport := func(q ImportedQuestion) int64 {
		var id int64
		err := s.InTx(fctx, func(db DBTX) error {
			var err error
			id, err = UpsertImportedQuestion(fctx, db, q)
			return err
		})
		if err != nil {
			t.Fatalf("import %s: %v", q.ImportKey, err)
		}
		return id
	}

	err = s.InTx(fctx, func(db DBTX) error {
		uid, err := UpsertUser(fctx, db, "admin@example.com", "An Admin")
		if err != nil {
			return err
		}
		admin = uid
		return AddMemberTx(fctx, db, familyID, uid, RoleAdmin)
	})
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}
	var subjectID int64
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		subjectID, err = UpsertSubject(fctx, db, Subject{
			Slug: "grandma", Kind: "individual", DisplayName: "Grandma", SortOrder: 1,
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	q := func(key, body string) ImportedQuestion {
		return ImportedQuestion{
			SubjectID: subjectID, AskedOfUserID: admin, Body: body,
			SortOrder: 1, ImportKey: key,
		}
	}
	reworded = reimport(q("k-reworded", "How did she and your mom get along?"))
	removed = reimport(q("k-removed", "What did she cook?"))
	untouched = reimport(q("k-untouched", "Where was she born?"))

	topic := "Childhood"
	const better = "How did Burton and your mother get along?"
	if err := s.EditQuestion(fctx, reworded, admin, better, &topic); err != nil {
		t.Fatalf("EditQuestion: %v", err)
	}
	if err := s.DeleteQuestion(fctx, removed, admin); err != nil {
		t.Fatalf("DeleteQuestion: %v", err)
	}

	// Removed means gone from the site immediately, by the same archived_at every
	// page already filters on.
	if _, err := s.Question(fctx, removed); !errors.Is(err, ErrNotFound) {
		t.Errorf("a removed question still reads back: %v", err)
	}

	// Now the import runs again with the file unchanged, exactly as it would on the
	// next deploy.
	reimport(q("k-reworded", "How did she and your mom get along?"))
	reimport(q("k-removed", "What did she cook?"))
	reimport(q("k-untouched", "Where was she born?"))

	got, err := s.Question(fctx, reworded)
	if err != nil {
		t.Fatalf("Question after reimport: %v", err)
	}
	if got.Body != better {
		t.Errorf("the import put the old wording back:\n got %q\nwant %q", got.Body, better)
	}
	if !got.Edited {
		t.Error("a reworded question does not say it was reworded")
	}
	if _, err := s.Question(fctx, removed); !errors.Is(err, ErrNotFound) {
		t.Errorf("the import brought a removed question back: %v", err)
	}

	// And the guard is narrow: a question nobody touched still takes its wording from
	// the file, which is the whole reason the importer overwrites in the first place.
	if err := s.EditQuestion(fctx, untouched, admin, "placeholder", nil); err != nil {
		t.Fatalf("EditQuestion untouched: %v", err)
	}
	reimport(q("k-untouched", "Where was she born, and who else was in the house?"))
	after, err := s.Question(fctx, untouched)
	if err != nil {
		t.Fatalf("Question untouched: %v", err)
	}
	if after.Body != "placeholder" {
		t.Errorf("edited question took the file's wording: %q", after.Body)
	}

	// Editing something already removed is a no-op rather than a resurrection.
	if err := s.EditQuestion(fctx, removed, admin, "back from the dead", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("EditQuestion on a removed question = %v, want ErrNotFound", err)
	}
}

// Removing a question keeps the answers to it.
//
// An answer is the thing this site exists to collect. A question that turned out to be
// badly worded, or that is upsetting, or that was asked of the wrong person, is a
// reason to take the question off the site -- never a reason to lose what somebody
// sat down and wrote.
func TestRemovingAQuestionKeepsItsAnswers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	familyID, err := s.CreateFamily(ctx, "answers", "The Answers line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{familyID}), familyID)

	var questionID, uid int64
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		uid, err = UpsertUser(fctx, db, "answerer@example.com", "An Answerer")
		if err != nil {
			return err
		}
		if err := AddMemberTx(fctx, db, familyID, uid, RoleAdmin); err != nil {
			return err
		}
		sid, err := UpsertSubject(fctx, db, Subject{
			Slug: "grandpa", Kind: "individual", DisplayName: "Grandpa", SortOrder: 1,
		})
		if err != nil {
			return err
		}
		questionID, err = UpsertImportedQuestion(fctx, db, ImportedQuestion{
			SubjectID: sid, AskedOfUserID: uid, Body: "What was he like?",
			SortOrder: 1, ImportKey: "k-answered",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	const written = "He kept every receipt he ever got, in a shoebox."
	if _, err := s.SaveAnswer(fctx, questionID, uid, written, false); err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}

	n, err := s.AnswerCountFor(fctx, questionID)
	if err != nil {
		t.Fatalf("AnswerCountFor: %v", err)
	}
	if n != 1 {
		t.Fatalf("answers before removal = %d, want 1", n)
	}

	if err := s.DeleteQuestion(fctx, questionID, uid); err != nil {
		t.Fatalf("DeleteQuestion: %v", err)
	}
	after, err := s.AnswerCountFor(fctx, questionID)
	if err != nil {
		t.Fatalf("AnswerCountFor after: %v", err)
	}
	if after != 1 {
		t.Errorf("removing the question destroyed %d answer(s)", 1-after)
	}
}

// The breadcrumb on the question page needs to know which line the question's
// subject belongs to, to link back to it correctly.
func TestQuestionCarriesItsFamily(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "carries", "The Carries line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{famID}), famID)

	var questionID int64
	err = s.InTx(fctx, func(db DBTX) error {
		uid, err := UpsertUser(fctx, db, "grandchild@example.com", "Grandchild")
		if err != nil {
			return err
		}
		if err := AddMemberTx(fctx, db, famID, uid, RoleContributor); err != nil {
			return err
		}
		subjectID, err := UpsertSubject(fctx, db, Subject{
			Slug: "grandma-rose", Kind: "individual", DisplayName: "Grandma Rose", SortOrder: 1,
		})
		if err != nil {
			return err
		}
		questionID, err = UpsertImportedQuestion(fctx, db, ImportedQuestion{
			SubjectID: subjectID, AskedOfUserID: uid,
			Body: "What was her garden like?", SortOrder: 1, ImportKey: "rose-1",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	q, err := s.Question(fctx, questionID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if q.FamilySlug != "carries" {
		t.Errorf("FamilySlug = %q, want %q", q.FamilySlug, "carries")
	}
}
