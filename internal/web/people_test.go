package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// Removing somebody takes away their way in and leaves what they wrote. A family
// history that deletes what a person told it is not a family history.
func TestRemovingSomebodyKeepsWhatTheyWrote(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")

	mom, err := h.store.UserByEmail(ctx, "mom@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}

	// She answers something first, so there is a record to protect.
	items, err := h.store.ListQuestions(ctx, mom.ID, store.QuestionFilter{AskedOfName: "Mom"})
	if err != nil || len(items) == 0 {
		t.Fatalf("no questions for Mom: %v", err)
	}
	if _, err := h.store.SaveAnswer(ctx, items[0].ID, mom.ID, "Her own words.", false); err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}

	rec := h.post("/people/remove", url.Values{
		"family":  {"home"},
		"user_id": {fmt.Sprint(mom.ID)},
	}, chris)
	if rec.Code != 303 {
		t.Fatalf("removing: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Gone from the family.
	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	if _, err := h.store.Member(store.WithFamily(ctx, fam.ID), fam.ID, mom.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("still a member after being removed: %v", err)
	}

	// And her words are still there.
	answers, err := h.store.AnswersTo(ctx, items[0].ID)
	if err != nil {
		t.Fatalf("AnswersTo: %v", err)
	}
	var kept bool
	for _, a := range answers {
		if a.Body == "Her own words." {
			kept = true
		}
	}
	if !kept {
		t.Error("removing somebody deleted what they had written")
	}
}

// Nobody removes themselves: it is almost always a misclick, and an admin who did
// it to their only family would be locked out of the thing they run.
func TestYouCannotRemoveYourself(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")
	me, err := h.store.UserByEmail(ctx, "chris@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}

	rec := h.post("/people/remove", url.Values{
		"family":  {"home"},
		"user_id": {fmt.Sprint(me.ID)},
	}, chris)
	if rec.Code != 400 {
		t.Errorf("removing yourself: status %d, want 400", rec.Code)
	}
}

// Changing the address somebody signs in with, which until now needed a terminal.
func TestChangingSomebodysAddress(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")
	dad, err := h.store.UserByEmail(ctx, "dad@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}

	rec := h.post("/people/email", url.Values{
		"family":  {"home"},
		"user_id": {fmt.Sprint(dad.ID)},
		"email":   {"dad@hisrealdomain.com"},
	}, chris)
	if rec.Code == 400 {
		t.Fatalf("changing an address: %s", rec.Body.String())
	}

	moved, err := h.store.UserByEmail(ctx, "dad@hisrealdomain.com")
	if err != nil {
		t.Fatalf("the address did not move: %v", err)
	}
	if moved.ID != dad.ID {
		t.Error("the address landed on a different account")
	}

	// Not a new person: his questions are still his.
	items, err := h.store.ListQuestions(ctx, moved.ID, store.QuestionFilter{AskedOfName: moved.DisplayName})
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(items) == 0 {
		t.Error("his questions were orphaned by the change of address")
	}
}

// A family runs itself: anybody in a line may sort out who is in it, because
// waiting on an admin to fix a mistyped address is the friction this is meant not
// to have. The one person they cannot reach is an admin -- otherwise whoever was
// added last could remove the person who runs the line, or point their sign-in
// link at their own inbox and read everything as them.
func TestAContributorManagesTheFamilyButNotItsAdmins(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dad := h.signIn("dad@example.com")

	chris, err := h.store.UserByEmail(ctx, "chris@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	mom, err := h.store.UserByEmail(ctx, "mom@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	famCtx := store.WithFamily(ctx, fam.ID)

	// Dad is a contributor. Chris runs the line.
	t.Run("cannot reach an admin", func(t *testing.T) {
		for _, c := range []struct {
			what string
			path string
			form url.Values
		}{
			{"removing the admin", "/people/remove", url.Values{
				"family": {"home"}, "user_id": {fmt.Sprint(chris.ID)}}},
			{"moving the admin's address", "/people/email", url.Values{
				"family": {"home"}, "user_id": {fmt.Sprint(chris.ID)},
				"email": {"attacker@example.com"}}},
		} {
			if rec := h.post(c.path, c.form, dad); rec.Code != http.StatusForbidden {
				t.Errorf("%s as a contributor: status %d, want 403", c.what, rec.Code)
			}
		}
		if _, err := h.store.Member(famCtx, fam.ID, chris.ID); err != nil {
			t.Errorf("a contributor removed the admin: %v", err)
		}
		if again, err := h.store.UserByEmail(ctx, "chris@example.com"); err != nil || again.ID != chris.ID {
			t.Error("a contributor moved the admin's sign-in address")
		}
	})

	t.Run("but manages everybody else", func(t *testing.T) {
		rec := h.post("/people/email", url.Values{
			"family": {"home"}, "user_id": {fmt.Sprint(mom.ID)},
			"email": {"mom@herrealdomain.com"}}, dad)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("a contributor should be able to fix another member's address")
		}
		if _, err := h.store.UserByEmail(ctx, "mom@herrealdomain.com"); err != nil {
			t.Errorf("the address did not move: %v", err)
		}

		if rec := h.post("/people/remove", url.Values{
			"family": {"home"}, "user_id": {fmt.Sprint(mom.ID)}}, dad); rec.Code == http.StatusForbidden {
			t.Error("a contributor should be able to remove another member")
		}
	})

	// And the controls follow the same rule, so nobody is offered a button that
	// will refuse them.
	t.Run("the page offers what it allows", func(t *testing.T) {
		body := h.get("/people", dad).Body.String()
		if strings.Contains(body, fmt.Sprintf(`name="user_id" value="%d"`, chris.ID)) {
			t.Error("a contributor is offered controls against the admin")
		}
	})
}

// Somebody already in the tree has their name recorded there, and being asked to
// type it again is how you end up with two versions of it.
func TestNameComesFromTheTree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")

	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	people, err := h.store.UnclaimedTreePeople(store.WithFamily(ctx, fam.ID), fam.ID)
	if err != nil {
		t.Fatalf("UnclaimedTreePeople: %v", err)
	}
	if len(people) == 0 {
		t.Skip("no unclaimed living people in the fixture tree")
	}
	who := people[0]

	// No name given at all.
	rec := h.post("/people", url.Values{
		"family":    {"home"},
		"email":     {"fromtree@example.com"},
		"person_id": {fmt.Sprint(who.ID)},
	}, chris)
	if rec.Code == 400 {
		t.Fatalf("adding somebody from the tree without typing a name: %s", rec.Body.String())
	}

	added, err := h.store.UserByEmail(ctx, "fromtree@example.com")
	if err != nil {
		t.Fatalf("the person was not added: %v", err)
	}
	if added.DisplayName != who.ShortName() {
		t.Errorf("named them %q, want %q from the tree", added.DisplayName, who.ShortName())
	}

	// With neither a name nor a person there is nothing to go on, and saying so is
	// better than inventing one from the address.
	rec = h.post("/people", url.Values{
		"family": {"home"}, "email": {"nobody@example.com"},
	}, chris)
	if rec.Code != 400 {
		t.Errorf("adding somebody with no name and no tree person: status %d, want 400", rec.Code)
	}
}

// The aunts, uncles and cousins are imported with nothing asked about them on
// purpose: the family writes their own questions for the people they actually
// remember. That makes this path the whole design, not a nicety -- reach somebody
// from the chart, ask something, and they join the lists.
//
// It also has to offer the right people to ask. Rosemary is an Ayres great-aunt,
// and her page was offering Frank, Tony, Inez, Robert and Violeta from Ashley's
// side; the handler behind the form then refused them.
func TestAddingToSomebodyNobodyHasAskedAbout(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")

	// A subject with nothing recorded about it, which is what a collateral is.
	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	famCtx := store.WithFamily(ctx, fam.ID)
	err = h.store.InTx(famCtx, func(db store.DBTX) error {
		_, err := store.UpsertSubject(famCtx, db, store.Subject{
			Slug: "great-aunt-may", Kind: "individual",
			DisplayName: "May (Hale) Corbett", SortOrder: 90,
			Relation: "sibling", Generation: 1,
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed subject: %v", err)
	}

	page := h.get("/subjects/great-aunt-may", chris).Body.String()
	if !strings.Contains(page, "Ask something about May (Hale) Corbett") {
		t.Error("no way to ask about somebody with nothing recorded yet")
	}
	if !strings.Contains(page, "Add a story about May (Hale) Corbett") {
		t.Error("no way to write a story about them either")
	}

	// Only people who answer in this line, or the form offers a choice the handler
	// will reject.
	offered := regexp.MustCompile(`name="asked_of" value="([^"]+)"`).FindAllStringSubmatch(page, -1)
	if len(offered) == 0 {
		t.Fatal("nobody is offered to answer")
	}
	contributors, err := h.store.Contributors(store.WithFamilies(ctx, []int64{fam.ID}), "home")
	if err != nil {
		t.Fatalf("Contributors: %v", err)
	}
	allowed := map[string]bool{}
	for _, c := range contributors {
		allowed[c.DisplayName] = true
	}
	for _, m := range offered {
		if !allowed[m[1]] {
			t.Errorf("the form offers %q, who does not answer in this line", m[1])
		}
	}

	// Asking puts them in the lists, which is the point.
	rec := h.post("/subjects/great-aunt-may/questions", url.Values{
		"asked_of": {offered[0][1]},
		"body":     {"What do you remember about her?"},
	}, chris)
	if rec.Code != 303 {
		t.Fatalf("asking about her: status %d, body %s", rec.Code, rec.Body.String())
	}

	subjects, err := h.store.SubjectsWithProgress(
		store.WithFamilies(ctx, []int64{fam.ID}), "", "home")
	if err != nil {
		t.Fatalf("SubjectsWithProgress: %v", err)
	}
	var found bool
	for _, s := range subjects {
		if s.Slug == "great-aunt-may" && s.AnyTotal == 1 {
			found = true
		}
	}
	if !found {
		t.Error("she is still not in the list of people something is recorded about")
	}
}

// The checkbox that offers who a question can be asked of must not require a
// question to already exist -- otherwise nobody added after the initial import
// could ever be asked their first thing. This is what was blocking Ashley, her
// cousin, and anybody else added since.
func TestNewContributorCanBeAskedImmediately(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")

	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	famCtx := store.WithFamily(ctx, fam.ID)

	rec := h.post("/people", url.Values{
		"family":       {"home"},
		"display_name": {"Cousin Ashley"},
		"email":        {"ashley@example.com"},
	}, chris)
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("adding a new contributor: %s", rec.Body.String())
	}

	page := h.get("/subjects/peter-samuel-hale", chris).Body.String()
	if !strings.Contains(page, `name="asked_of" value="Cousin Ashley"`) {
		t.Error("a brand-new contributor should be offered as someone to ask, with nothing asked of them yet")
	}

	rec = h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"asked_of": {"Cousin Ashley"}, "body": {"What games did you play with him?"},
	}, chris)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("asking the new contributor: status %d, %s", rec.Code, rec.Body.String())
	}

	ashley, err := h.store.UserByEmail(ctx, "ashley@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	count, err := h.store.CountQuestionsFor(famCtx, ashley.ID)
	if err != nil {
		t.Fatalf("CountQuestionsFor: %v", err)
	}
	if count != 1 {
		t.Errorf("Cousin Ashley has %d questions, want 1", count)
	}
}
