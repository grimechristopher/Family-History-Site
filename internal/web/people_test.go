package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/store"
)

// The add form has to name the line it is adding to.
//
// It did not, and once Chris belonged to four families the handler refused every
// submission -- "Pick which family to add them to" -- with nothing on the page
// that could have picked one. Adding anybody was impossible and the page gave no
// clue why.
func TestAddPersonFormNamesTheLine(t *testing.T) {
	h := newHarness(t)
	seedOtherFamily(t, h)

	// Chris is in both, which is the case that broke.
	ctx := context.Background()
	other, err := h.store.FamilyBySlug(ctx, "other")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	admin := h.signIn("chris@example.com")
	adminID, err := h.store.UserByEmail(ctx, "chris@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if err := h.store.AddMember(store.WithFamily(ctx, other.ID), other.ID, adminID.ID, store.RoleAdmin); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	body := h.get("/people", admin).Body.String()
	if !strings.Contains(body, `name="family"`) {
		t.Fatal("the add form must carry the line it is adding to")
	}

	// And the submission it describes must actually be accepted.
	rec := h.post("/people", url.Values{
		"family":       {"other"},
		"display_name": {"Great Aunt Ida"},
		"email":        {"ida@example.com"},
	}, admin)
	// Not 400: that is the "Pick which family to add them to" refusal, which is
	// what an unsubmittable form produced. Supabase is not reachable from a test,
	// so the handler reports the sign-in could not be set up and still saves the
	// membership, which is the part being checked here.
	if rec.Code == 400 {
		t.Fatalf("the form the page renders was refused: %s", rec.Body.String())
	}

	members, err := h.store.Members(store.WithFamily(ctx, other.ID), other.ID)
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	var found bool
	for _, m := range members {
		if m.DisplayName == "Great Aunt Ida" {
			found = true
		}
	}
	if !found {
		t.Error("the person added is not in the family")
	}
}

// The tree picker offers people who might sign in. A birth year of 1860 and a
// death year of 1925 is not somebody who is going to answer questions.
func TestTreePickerOffersOnlyTheLiving(t *testing.T) {
	h := newHarness(t)
	s := h.store
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "grime", "The Grime line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	other, err := s.CreateFamily(ctx, "lucero", "The Lucero line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}

	type row struct {
		family          int64
		given           string
		birth, death    *int
		shouldBeOffered bool
	}
	y := func(n int) *int { return &n }
	rows := []row{
		{famID, "Living Grandson", y(1986), nil, true},
		{famID, "No Dates Recorded", nil, nil, true},
		{famID, "Died In 1925", y(1860), y(1925), false},
		{famID, "Born In 1870, No Death Recorded", y(1870), nil, false},
		{other, "Somebody In The Other Line", y(1990), nil, false},
	}
	for _, r := range rows {
		fctx := store.WithFamily(ctx, r.family)
		err := s.InTx(fctx, func(db store.DBTX) error {
			_, err := store.UpsertPerson(fctx, db, store.Person{
				GedcomID: "@" + r.given + "@", Given: r.given, Surname: "Test",
				BirthYear: r.birth, DeathYear: r.death,
			})
			return err
		})
		if err != nil {
			t.Fatalf("seed %s: %v", r.given, err)
		}
	}

	offered, err := s.UnclaimedTreePeople(store.WithFamily(ctx, famID), famID)
	if err != nil {
		t.Fatalf("UnclaimedTreePeople: %v", err)
	}
	got := map[string]bool{}
	for _, p := range offered {
		got[p.Given] = true
	}
	for _, r := range rows {
		if got[r.given] != r.shouldBeOffered {
			if r.shouldBeOffered {
				t.Errorf("%s should be offered and was not", r.given)
			} else {
				t.Errorf("%s should not be offered and was", r.given)
			}
		}
	}
}

// One question is often for several people: the four Lucero siblings remember
// their parents differently and all four should be asked. The form takes as many
// as you tick, and every one of them gets it in their own card stack.
func TestAskingSeveralPeopleAtOnce(t *testing.T) {
	h := newHarness(t)
	chris := h.signIn("chris@example.com")

	body := h.get("/subjects/peter-samuel-hale", chris).Body.String()
	if !strings.Contains(body, `type="checkbox" name="asked_of"`) {
		t.Error("the form should let you tick more than one person")
	}

	rec := h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"asked_of": {"Dad", "Mom"},
		"body":     {"What did the two of you make of him at first?"},
	}, chris)
	if rec.Code != 303 {
		t.Fatalf("asking: status %d, body %s", rec.Code, rec.Body.String())
	}

	// It is one question, and it is in both stacks.
	ctx := context.Background()
	for _, who := range []string{"dad@example.com", "mom@example.com"} {
		u, err := h.store.UserByEmail(ctx, who)
		if err != nil {
			t.Fatalf("UserByEmail %s: %v", who, err)
		}
		items, err := h.store.ListQuestions(ctx, u.ID, store.QuestionFilter{AskedOfName: u.DisplayName})
		if err != nil {
			t.Fatalf("ListQuestions: %v", err)
		}
		var found bool
		for _, q := range items {
			if strings.HasPrefix(q.Body, "What did the two of you make") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was ticked but the question is not in their list", u.DisplayName)
		}
	}

	// Nobody ticked is a mistake worth saying so about, not an unasked question.
	rec = h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"body": {"A question for nobody."},
	}, chris)
	if rec.Code != 400 {
		t.Errorf("a question with nobody to answer it: status %d, want 400", rec.Code)
	}
}
