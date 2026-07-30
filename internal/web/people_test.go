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
