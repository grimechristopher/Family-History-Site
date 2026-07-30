package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/store"
)

// These are the tests that prove the guarantee rather than the code, and they are
// the only ones that connect as the unprivileged role. Every other test in this
// package connects as the owner, for whom row-level security is skipped unless
// FORCE is set -- so an isolation test that ran as the owner could pass with the
// policies missing entirely.

// appServer builds a second server whose pool connects as fhs_app, the role the
// deployment uses. The schema is already migrated by the harness.
func appServer(t *testing.T, h *harness) (http.Handler, *store.Store) {
	t.Helper()

	admin := os.Getenv("TEST_DATABASE_URL")
	appURL := strings.Replace(admin, "//postgres:", "//fhs_app:", 1)
	if appURL == admin {
		t.Fatalf("could not derive the app role URL from %q", admin)
	}

	pool, err := pgxpool.New(context.Background(), appURL)
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	s := store.New(pool)
	cfg := h.server.Config
	srv, err := New(cfg, s, slog.New(slog.NewTextHandler(os.Stderr, nil)), "test")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return srv.Routes(), s
}

// seedOtherFamily creates a second family with its own subject, question and
// answer, and a member who is in it and nothing else.
func seedOtherFamily(t *testing.T, h *harness) (familyID int64, questionID int64) {
	t.Helper()
	ctx := context.Background()

	familyID, err := h.store.CreateFamily(ctx, "other", "Another family")
	if err != nil {
		t.Fatalf("create other family: %v", err)
	}
	other := store.WithFamily(ctx, familyID)

	err = h.store.InTx(other, func(db store.DBTX) error {
		uid, err := store.UpsertUser(other, db, "aunt@example.com", "Aunt")
		if err != nil {
			return err
		}
		if err := store.AddMemberTx(other, db, familyID, uid, store.RoleContributor); err != nil {
			return err
		}
		sub, err := store.UpsertSubject(other, db, store.Subject{
			Slug: "her-father", Kind: "individual", DisplayName: "Her father", SortOrder: 1,
		})
		if err != nil {
			return err
		}
		questionID, err = store.UpsertImportedQuestion(other, db, store.ImportedQuestion{
			SubjectID: sub, AskedOfUserID: uid,
			Body: "A QUESTION IN THE OTHER FAMILY", SortOrder: 1, ImportKey: "other-1",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed other family: %v", err)
	}
	return familyID, questionID
}

// A member of one family must see nothing of another from any page that lists,
// and nothing by asking for it directly either -- which is the case a list-only
// test would miss.
func TestOneFamilySeesNothingOfAnother(t *testing.T) {
	h := newHarness(t)
	_, otherQuestion := seedOtherFamily(t, h)

	handler, _ := appServer(t, h)
	cookie := h.signIn("mom@example.com") // a member of "home" only

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Her own family first. Without this the checks below would pass just as well
	// on a page that was broken, empty, or a 500 -- which is the way an isolation
	// test quietly stops testing anything.
	own := get("/f/home/questions")
	if own.Code != http.StatusOK {
		t.Fatalf("her own questions page = %d, want 200", own.Code)
	}
	if !strings.Contains(own.Body.String(), "How did they meet?") {
		t.Fatalf("her own questions page is not showing her own questions, so the "+
			"leak checks below would prove nothing. body: %s", own.Body.String()[:400])
	}

	for _, path := range []string{
		"/f/home/questions", "/f/home/cards", "/f/home/subjects", "/f/home/tree",
	} {
		rec := get(path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200; a failing page cannot demonstrate isolation", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if strings.Contains(body, "A QUESTION IN THE OTHER FAMILY") {
			t.Errorf("%s leaked another family's question", path)
		}
		if strings.Contains(body, "Her father") {
			t.Errorf("%s leaked another family's subject", path)
		}
	}

	// By id, inside her own family's URL: the row exists, but not for her.
	rec := get(fmt.Sprintf("/f/home/questions/%d", otherQuestion))
	if rec.Code != http.StatusNotFound {
		t.Errorf("fetching another family's question by id = %d, want 404", rec.Code)
	}
}

// Somebody who is not a member must not be able to tell that a family exists, so
// this is 404 and not 403.
func TestNonMemberGetsNotFoundNotForbidden(t *testing.T) {
	h := newHarness(t)
	seedOtherFamily(t, h)

	handler, _ := appServer(t, h)
	cookie := h.signIn("mom@example.com")

	for _, path := range []string{"/f/other/questions", "/f/other/", "/f/other/tree"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as a non-member = %d, want 404", path, rec.Code)
		}
	}
}

// A family that does not exist is indistinguishable from one you are not in.
func TestUnknownFamilyIsAlsoNotFound(t *testing.T) {
	h := newHarness(t)
	handler, _ := appServer(t, h)
	cookie := h.signIn("mom@example.com")

	req := httptest.NewRequest(http.MethodGet, "/f/no-such-family/questions", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET an unknown family = %d, want 404", rec.Code)
	}
}

// The backstop: as the role the server actually uses, with no family set, a query
// that forgot to scope itself sees nothing. If this fails, every isolation policy
// is inert and the tests above are passing for the wrong reason.
func TestTheAppRoleSeesNothingWithoutAFamily(t *testing.T) {
	h := newHarness(t)
	seedOtherFamily(t, h)

	_, s := appServer(t, h)

	var n int
	err := s.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM family.questions`).Scan(&n)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("an unscoped read saw %d questions; row-level security is not in force", n)
	}
}

// Adding somebody is three writes that have to happen together: an identity, a
// membership, and the link saying who they are on the tree. The link is the one
// that broke -- it was run on the pool while the membership was still uncommitted
// in the request's transaction, so it updated nothing and reported success.
func TestAddingSomeoneLinksThemToTheTree(t *testing.T) {
	h := newHarness(t)
	// The harness deliberately starts without one, so photo uploads can be tested
	// degrading. Adding somebody needs it: without a service key their Supabase
	// account cannot be made, and the handler saves the membership but warns.
	h.server.Config.SupabaseServiceKey = "service-key"

	handler, _ := appServer(t, h)
	cookie := h.signIn("chris@example.com")

	// Somebody in this family's tree for her to be. Seeded here rather than looked
	// up, so the test cannot quietly skip when the fixture has no tree.
	var personID int64
	err := h.store.Pool.QueryRow(h.ctx, `
		INSERT INTO family.people (family_id, gedcom_id, given_name, surname)
		VALUES ($1, '@JANE@', 'Jane', 'Hale') RETURNING id`, h.familyID).Scan(&personID)
	if err != nil {
		t.Fatalf("seed a person for her to be: %v", err)
	}

	form := url.Values{
		"display_name": {"Aunt Jane"},
		"email":        {"jane@example.com"},
		"person_id":    {strconv.FormatInt(personID, 10)},
	}
	req := httptest.NewRequest(http.MethodPost, "/f/home/people",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Supabase is stubbed, so account creation succeeds and this should redirect.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("adding somebody = %d, want 303. body: %s", rec.Code, rec.Body.String())
	}

	var gotPerson *int64
	var role string
	err = h.store.Pool.QueryRow(h.ctx, `
		SELECT m.person_id, m.role
		  FROM core.family_members m
		  JOIN core.users u ON u.id = m.user_id
		 WHERE m.family_id = $1 AND u.email = 'jane@example.com'`, h.familyID).
		Scan(&gotPerson, &role)
	if err != nil {
		t.Fatalf("she is not a member: %v", err)
	}
	if role != "contributor" {
		t.Errorf("role = %q, want contributor", role)
	}
	if gotPerson == nil {
		t.Fatal("she was added but not linked to the tree; the link ran outside the transaction")
	}
	if *gotPerson != personID {
		t.Errorf("linked to person %d, want %d", *gotPerson, personID)
	}
}
