package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/config"
	"github.com/grimechristopher/family-history-site/internal/migrate"
	"github.com/grimechristopher/family-history-site/internal/storage"
	"github.com/grimechristopher/family-history-site/internal/store"
)

const jwtSecret = "test-jwt-secret-for-web-handlers"

type harness struct {
	t           *testing.T
	server      *Server
	handler     http.Handler
	store       *store.Store
	dadID       int64
	momID       int64
	dadQuestion int64
	momQuestion int64
	familyID    int64
	familySlug  string
	ctx         context.Context
	sentEmails  []string
	// liveCode is the one six-digit code the stubbed Supabase will accept.
	liveCode string
	// sendRateLimited makes the stub answer a send with 429, as Supabase does when
	// the same address asks twice in quick succession.
	sendRateLimited bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip(`TEST_DATABASE_URL not set; run: eval "$(scripts/testdb.sh start)"`)
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS family CASCADE; DROP SCHEMA IF EXISTS core CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := migrate.Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := store.New(pool)

	h := &harness{t: t, store: s}

	// Stand in for Supabase so no test ever reaches the network.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Email string `json:"email"`
			Token string `json:"token"`
		}
		_ = json.Unmarshal(body, &payload)

		// The code exchange, which answers with a token only for the code the test
		// says is live. Everything else is a send, recorded and acknowledged.
		if strings.HasSuffix(r.URL.Path, "/auth/v1/verify") {
			if h.liveCode == "" || payload.Token != h.liveCode {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"error_code":"otp_expired","msg":"Token has expired or is invalid"}`)
				return
			}
			// One use only, as Supabase does it.
			h.liveCode = ""
			token := mintToken(t, jwtSecret, payload.Email, "authenticated", time.Now().Add(time.Hour))
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"access_token":"`+token+`","token_type":"bearer"}`)
			return
		}

		if h.sendRateLimited {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"code":429,"error_code":"over_email_send_rate_limit",`+
				`"msg":"For security purposes, you can only request this after 52 seconds."}`)
			return
		}
		h.sentEmails = append(h.sentEmails, payload.Email)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "{}")
	}))
	t.Cleanup(fake.Close)

	cfg := config.Config{
		DatabaseURL:       url,
		SupabaseURL:       fake.URL,
		SupabaseAnonKey:   "anon-key",
		SupabaseJWTSecret: jwtSecret,
		BaseURL:           "http://localhost:8099",
		Addr:              ":0",
	}
	srv, err := New(cfg, s, slog.New(slog.NewTextHandler(os.Stderr, nil)), "test")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	h.server = srv
	h.handler = srv.Routes()

	// The family everything is seeded into. The migrations leave a fresh database
	// with no families at all -- the one they create to hold pre-existing data is
	// removed when there is none -- so the harness makes its own.
	famID, err := s.CreateFamily(ctx, "home", "Our family")
	if err != nil {
		t.Fatalf("create the fixture family: %v", err)
	}
	fam := &store.Family{ID: famID, Slug: "home", DisplayName: "Our family"}
	h.familyID = fam.ID
	h.familySlug = fam.Slug
	ctx = store.WithFamily(ctx, fam.ID)
	h.ctx = ctx

	// Seed two contributors, each with a question of their own.
	err = s.InTx(ctx, func(db store.DBTX) error {
		dad, err := store.UpsertUser(ctx, db, "dad@example.com", "Dad")
		if err != nil {
			return err
		}
		mom, err := store.UpsertUser(ctx, db, "mom@example.com", "Mom")
		if err != nil {
			return err
		}
		chris, err := store.UpsertUser(ctx, db, "chris@example.com", "Chris")
		if err != nil {
			return err
		}
		for id, role := range map[int64]string{
			dad: store.RoleContributor, mom: store.RoleContributor, chris: store.RoleAdmin,
		} {
			if err := store.AddMemberTx(ctx, db, fam.ID, id, role); err != nil {
				return err
			}
		}
		h.dadID, h.momID = dad, mom

		subject, err := store.UpsertSubject(ctx, db, store.Subject{
			Slug: "peter-samuel-hale", Kind: "individual",
			DisplayName: "Peter Samuel Hale", SortOrder: 1,
		})
		if err != nil {
			return err
		}
		h.dadQuestion, err = store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID: subject, AskedOfUserID: dad,
			Body: "What kind of cars did he have?", SortOrder: 1, ImportKey: "dad-1",
		})
		if err != nil {
			return err
		}
		_, err = store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID: subject, AskedOfUserID: dad,
			Body: "What were his favorite meals?", SortOrder: 2, ImportKey: "dad-2",
		})
		if err != nil {
			return err
		}
		h.momQuestion, err = store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID: subject, AskedOfUserID: mom,
			Body: "How did they meet?", SortOrder: 3, ImportKey: "mom-1",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return h
}

// mintToken produces a Supabase-shaped access token.
func mintToken(t *testing.T, secret, email, role string, exp time.Time) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := enc(map[string]any{
		"sub":   subjectFor(email),
		"email": email, "role": role, "iss": "supabase", "exp": exp.Unix(),
	})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// subjectFor derives a stable, distinct UUID per email, mirroring the fact that
// real Supabase gives each account its own identity. supabase_user_id is UNIQUE,
// so sharing one subject across users would collide.
func subjectFor(email string) string {
	sum := sha256.Sum256([]byte(email))
	hexed := hex.EncodeToString(sum[:16])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

func (h *harness) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// inFamily prefixes the paths that live under a family, so the existing tests can
// go on naming "/cards" and mean this harness's family. Anything already absolute
// about a family, and everything outside one -- login, the callback, the invite
// pages -- is left alone.
func (h *harness) inFamily(path string) string {
	if strings.HasPrefix(path, "/f/") {
		return path
	}
	// "/" means this family's home page. The bare root is a chooser that redirects,
	// and no test is about that.
	if path == "/" {
		return "/f/" + h.familySlug + "/"
	}
	for _, p := range []string{"/cards", "/questions", "/entries", "/stories",
		"/photos", "/tree", "/subjects"} {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+"?") {
			return "/f/" + h.familySlug + path
		}
	}
	return path
}

func (h *harness) post(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	path = h.inFamily(path)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return h.do(req)
}

func (h *harness) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	path = h.inFamily(path)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return h.do(req)
}

// signIn exchanges a minted token for a session cookie.
func (h *harness) signIn(email string) *http.Cookie {
	h.t.Helper()
	token := mintToken(h.t, jwtSecret, email, "authenticated", time.Now().Add(time.Hour))
	rec := h.post("/auth/session", url.Values{"access_token": {token}}, nil)
	if rec.Code != http.StatusSeeOther {
		h.t.Fatalf("sign in as %s: status %d, body %s", email, rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	h.t.Fatalf("no session cookie issued for %s", email)
	return nil
}

// --- auth ---------------------------------------------------------------

// Asking twice in a row is the likeliest way for somebody to meet Supabase's send
// limit, and the first email is already sitting in their inbox. Sending them back
// to the address form would take away the code field that would let them in.
func TestAskingTwiceStillOffersTheCodeField(t *testing.T) {
	h := newHarness(t)
	h.sendRateLimited = true

	rec := h.post("/login", url.Values{"email": {"mom@example.com"}}, nil)
	body := rec.Body.String()

	if !strings.Contains(body, `action="/login/code"`) {
		t.Error("the code field is gone, so a working code in the inbox cannot be used")
	}
	if !strings.Contains(body, "already on its way") {
		t.Errorf("no explanation that an email was already sent. body: %s", body)
	}
	if strings.Contains(body, "couldn't send") {
		t.Error("reported as a failure when an email had in fact just been sent")
	}
	// Quietly, since nothing went wrong.
	if !strings.Contains(body, "banner-quiet") {
		t.Error("styled as an error when it is only information")
	}
}

// The sign-in email offers a six-digit code as well as a link, so the site has to
// take it. It is also the route that does not depend on the callback URL being in
// Supabase's redirect allow list, which is what broke sign-in on the LAN.
func TestSignInWithTheCodeFromTheEmail(t *testing.T) {
	h := newHarness(t)
	h.liveCode = "824669"

	// Typed the way somebody reads it off a screen.
	rec := h.post("/login/code", url.Values{
		"email": {"mom@example.com"},
		"code":  {" 824 669 "},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code sign-in = %d, want 303. body: %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie issued")
	}
	// The session must really be Mom's, not merely present.
	home := h.get("/", cookie)
	if !strings.Contains(home.Body.String(), "Mom") {
		t.Error("signed in, but the page is not Mom's")
	}
}

func TestCodeSignInRefusesWhatItShould(t *testing.T) {
	cases := []struct {
		name, email, code, live string
	}{
		{"wrong code", "mom@example.com", "111111", "824669"},
		{"no code at all", "mom@example.com", "", "824669"},
		{"address not on the allowlist", "stranger@example.com", "824669", "824669"},
		{"code already used", "mom@example.com", "824669", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.liveCode = tc.live

			rec := h.post("/login/code", url.Values{
				"email": {tc.email}, "code": {tc.code},
			}, nil)

			for _, c := range rec.Result().Cookies() {
				if c.Name == auth.SessionCookie && c.Value != "" {
					t.Fatalf("a session was issued for %s", tc.name)
				}
			}
			// Sent back to the form with something to act on, not a bare error.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 with the form again", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `class="banner"`) {
				t.Error("no explanation shown")
			}
		})
	}
}

// The dev login signs in as anybody with no link at all, so the thing worth
// pinning down is that it does not exist unless DEV_LOGIN was set.
func TestDevLoginExistsOnlyWhenConfigured(t *testing.T) {
	h := newHarness(t)

	rec := h.get("/dev/login/Mom", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("with DEV_LOGIN unset, GET /dev/login/Mom = %d, want 404", rec.Code)
	}

	// Same server, same store, the flag on.
	cfg := h.server.Config
	cfg.DevLogin = true
	srv, err := New(cfg, h.store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	dev := srv.Routes()

	rec = httptest.NewRecorder()
	dev.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev/login/Mom", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("with DEV_LOGIN on, GET /dev/login/Mom = %d, want 303", rec.Code)
	}
	var signedIn bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			signedIn = true
		}
	}
	if !signedIn {
		t.Error("no session cookie issued")
	}

	// A name that is not a contributor must not mint a session.
	rec = httptest.NewRecorder()
	dev.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dev/login/Nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /dev/login/Nobody = %d, want 404", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Error("a session was issued for a name that does not exist")
		}
	}
}

func TestProtectedPagesRedirectWhenSignedOut(t *testing.T) {
	h := newHarness(t)

	for _, path := range []string{"/", "/cards"} {
		rec := h.get(path, nil)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want 303", path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("GET %s redirected to %q, want /login", path, loc)
		}
	}
}

// htmx swallows a 302 into the swap target, so an expired session mid-stack must
// come back as HX-Redirect or the login page would render inside the card.
func TestExpiredSessionOnHtmxRequestUsesHXRedirect(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodPost, h.inFamily("/cards/1/defer"), nil)
	req.Header.Set("HX-Request", "true")
	rec := h.do(req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with an HX-Redirect header", rec.Code)
	}
	if got := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(got, "/login") {
		t.Errorf("HX-Redirect = %q, want /login", got)
	}
}

func TestSignInIssuesSessionAndBackfillsSupabaseID(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	if !cookie.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}

	u, err := h.store.UserByEmail(h.ctx, "dad@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	if u.SupabaseUserID == nil {
		t.Error("Supabase id should be backfilled on first login")
	}

	if rec := h.get("/", cookie); rec.Code != http.StatusOK {
		t.Errorf("GET / = %d, want 200", rec.Code)
	}
}

// A valid Supabase login that is not on the allowlist. This happens for real:
// the same Supabase project backs a public portfolio anyone can sign up to.
func TestVerifiedLoginNotOnAllowlistIsRefusedWithAnExplanation(t *testing.T) {
	h := newHarness(t)
	token := mintToken(t, jwtSecret, "stranger@example.com", "authenticated", time.Now().Add(time.Hour))

	rec := h.post("/auth/session", url.Values{"access_token": {token}}, nil)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "just for family") {
		t.Errorf("expected a plain explanation, got: %s", body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Error("no session may be issued for a non-allowlisted address")
		}
	}
}

func TestSessionEndpointRejectsBadTokens(t *testing.T) {
	h := newHarness(t)

	cases := map[string]string{
		"anon role":    mintToken(t, jwtSecret, "dad@example.com", "anon", time.Now().Add(time.Hour)),
		"service role": mintToken(t, jwtSecret, "dad@example.com", "service_role", time.Now().Add(time.Hour)),
		"wrong secret": mintToken(t, "not-the-secret", "dad@example.com", "authenticated", time.Now().Add(time.Hour)),
		"expired":      mintToken(t, jwtSecret, "dad@example.com", "authenticated", time.Now().Add(-time.Minute)),
		"garbage":      "not.a.token",
	}
	for name, token := range cases {
		rec := h.post("/auth/session", url.Values{"access_token": {token}}, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}

	if rec := h.post("/auth/session", url.Values{}, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing token: status = %d, want 400", rec.Code)
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	if rec := h.post("/logout", url.Values{}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if rec := h.get("/cards", cookie); rec.Code != http.StatusSeeOther {
		t.Error("the old cookie should no longer work")
	}
}

// An unknown address must look identical to a known one, so the page never
// reveals who is on the family list.
func TestLoginDoesNotRevealWhoIsOnTheAllowlist(t *testing.T) {
	h := newHarness(t)

	known := h.post("/login", url.Values{"email": {"dad@example.com"}}, nil)
	unknown := h.post("/login", url.Values{"email": {"stranger@example.com"}}, nil)

	if known.Code != http.StatusOK || unknown.Code != http.StatusOK {
		t.Fatalf("statuses = %d and %d", known.Code, unknown.Code)
	}
	for _, rec := range []*httptest.ResponseRecorder{known, unknown} {
		if !strings.Contains(rec.Body.String(), "Check your email") {
			t.Error("both outcomes must say the same thing")
		}
	}
	// Only the allowlisted address should actually provoke an email.
	if len(h.sentEmails) != 1 || h.sentEmails[0] != "dad@example.com" {
		t.Errorf("emails sent = %v, want only dad@example.com", h.sentEmails)
	}
}

// --- cards --------------------------------------------------------------

func TestCardsRendersTheTopQuestion(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	rec := h.get("/cards", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"What kind of cars did he have?", // first by sort_order
		"Peter Samuel Hale",              // the subject
		"data-draft-url",                 // autosave wired up
		"data-later",                     // the Later button exists without JS
		"Save &amp; next",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card page missing %q", want)
		}
	}
	// Mom's question must not appear in Dad's stack.
	if strings.Contains(body, "How did they meet?") {
		t.Error("another person's question leaked into this stack")
	}
}

func TestDeferAdvancesToTheNextCard(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	rec := h.post("/cards/"+strconv.FormatInt(h.dadQuestion, 10)+"/defer", url.Values{}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, "What were his favorite meals?") {
		t.Error("expected the next question after deferring")
	}
	if !strings.Contains(body, "come round again") {
		t.Error("expected reassurance that nothing was lost")
	}
	// A fragment, not a whole document, since htmx swaps the region.
	if strings.Contains(body, "<!doctype html>") {
		t.Error("htmx target should receive a fragment, not a full page")
	}
}

func TestAnswerSavesAndRemovesTheCard(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	rec := h.post("/cards/"+id+"/answer",
		url.Values{"body": {"A Studebaker, and later a Buick."}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "What kind of cars did he have?") {
		t.Error("the answered question should be gone from the stack")
	}

	e, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	if e.IsDraft {
		t.Error("an explicit save must not be stored as a draft")
	}
	if e.Body != "A Studebaker, and later a Buick." {
		t.Errorf("Body = %q", e.Body)
	}
}

// A mis-tap on "Save & next" must not record a blank answer, which would remove
// the question from the pile for good.
func TestEmptyAnswerIsTreatedAsDeferral(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	rec := h.post("/cards/"+id+"/answer", url.Values{"body": {"   \n  "}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "kept that one in the pile") {
		t.Errorf("expected a gentle explanation, got: %s", rec.Body.String())
	}
	if _, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID); err != store.ErrNotFound {
		t.Error("no entry should have been created for an empty save")
	}
}

func TestDraftSavesWithoutSwappingAnything(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	rec := h.post("/cards/"+id+"/draft", url.Values{"body": {"He drove a Stude"}}, cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Error("the draft endpoint must return no body: swapping mid-sentence would be hostile")
	}

	e, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	if !e.IsDraft {
		t.Error("autosave must store a draft")
	}

	// The draft comes back on the card so nothing typed is ever lost.
	if !strings.Contains(h.get("/cards", cookie).Body.String(), "He drove a Stude") {
		t.Error("draft did not survive a reload")
	}
}

// A stray autosave must never demote a finished answer back to a draft, which
// would quietly return it to the queue and undo the progress count.
func TestDraftDoesNotDemoteAPublishedAnswer(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	h.post("/cards/"+id+"/answer", url.Values{"body": {"final answer"}}, cookie)
	h.post("/cards/"+id+"/draft", url.Values{"body": {"final answer plus a bit"}}, cookie)

	e, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	if e.IsDraft {
		t.Error("a published answer must stay published")
	}
	p, _ := h.store.Progress(h.ctx, h.dadID)
	if p.Answered != 1 {
		t.Errorf("Answered = %d, want 1", p.Answered)
	}
}

// Everyone may read everything, but only the person a question was asked of may
// answer or defer it.
func TestCannotTouchAnotherPersonsQuestion(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	momQ := strconv.FormatInt(h.momQuestion, 10)

	for _, path := range []string{"/cards/" + momQ + "/defer", "/cards/" + momQ + "/answer", "/cards/" + momQ + "/draft"} {
		rec := h.post(path, url.Values{"body": {"not mine"}}, cookie)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestMissingQuestionIsNotFound(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	if rec := h.post("/cards/999999/defer", url.Values{}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec := h.post("/cards/not-a-number/defer", url.Values{}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetModePersistsAndValidates(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	rec := h.post("/cards/mode", url.Values{"mode": {"shuffle"}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	m, err := h.store.MembershipOf(h.ctx, h.familyID, h.dadID)
	if err != nil {
		t.Fatalf("MembershipOf: %v", err)
	}
	if m.QueueMode != store.QueueShuffle {
		t.Errorf("QueueMode = %q, want shuffle", m.QueueMode)
	}

	if rec := h.post("/cards/mode", url.Values{"mode": {"nonsense"}}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown mode: status = %d, want 400", rec.Code)
	}
	if rec := h.post("/cards/mode",
		url.Values{"mode": {"one_subject"}, "subject": {"no-such-slug"}}, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown subject: status = %d, want 400", rec.Code)
	}
}

func TestOneSubjectModeShowsTheSubjectPicker(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	rec := h.post("/cards/mode",
		url.Values{"mode": {"one_subject"}, "subject": {"peter-samuel-hale"}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Peter Samuel Hale") {
		t.Error("expected the subject picker to list the focused subject")
	}
}

func TestProgressIsFramedAsAccumulation(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	h.post("/cards/"+strconv.FormatInt(h.dadQuestion, 10)+"/answer",
		url.Values{"body": {"answered"}}, cookie)

	body := h.get("/", cookie).Body.String()
	if !strings.Contains(body, "answered <strong>1</strong>") {
		t.Errorf("expected an accumulating count, got: %s", body)
	}
	// Never a countdown of what is left to do.
	if strings.Contains(body, "remaining") {
		t.Error("progress must not be framed as a backlog")
	}
}

// --- plumbing -----------------------------------------------------------

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	rec := h.get("/healthz", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestSecurityHeadersAndStaticAssets(t *testing.T) {
	h := newHarness(t)

	rec := h.get("/login", nil)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options")
	}

	// htmx must be served locally, since the CSP forbids outside origins.
	if rec := h.get("/static/js/htmx.min.js", nil); rec.Code != http.StatusOK {
		t.Errorf("htmx not served locally: status %d", rec.Code)
	}
	if rec := h.get("/static/css/app.css?v=test", nil); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("versioned assets should be cached hard")
	}
}

func TestEveryTemplateParses(t *testing.T) {
	h := newHarness(t)
	for _, page := range []string{"home", "login", "denied", "callback", "cards"} {
		if _, ok := h.server.templates[page]; !ok {
			t.Errorf("template %q was not parsed", page)
		}
	}
}

// --- phase 2: list, others, replies, stories ----------------------------

// counts reads the numbers off the waiting/answered toggle.
func segmentCounts(body string) (waiting, answered string) {
	re := regexp.MustCompile(`(?s)show=waiting.*?segment-count">(\d+)<`)
	if m := re.FindStringSubmatch(body); m != nil {
		waiting = m[1]
	}
	re = regexp.MustCompile(`(?s)show=answered.*?segment-count">(\d+)<`)
	if m := re.FindStringSubmatch(body); m != nil {
		answered = m[1]
	}
	return
}

// The page shows one section at a time. Stacking a hundred and fifty waiting
// questions above the handful that were answered meant nobody ever reached them.
func TestWaitingAndAnsweredAreSeparateViews(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	// Defaults to what is still waiting: Dad has two of the three seeded.
	body := h.get("/questions", cookie).Body.String()
	waiting, answered := segmentCounts(body)
	if waiting != "2" || answered != "0" {
		t.Errorf("toggle showed %s waiting / %s answered, want 2 / 0", waiting, answered)
	}
	if !strings.Contains(body, `show=waiting" aria-pressed="true"`) &&
		!strings.Contains(body, `aria-pressed="true"`) {
		t.Error("the waiting segment should be the selected one by default")
	}
	if strings.Contains(body, "How did they meet?") {
		t.Error("Mom's question should not appear on Dad's list")
	}

	h.post("/questions/"+strconv.FormatInt(h.dadQuestion, 10)+"/answer",
		url.Values{"body": {"A Studebaker."}}, cookie)

	// The counts move, and the answered one is no longer in the waiting view.
	body = h.get("/questions", cookie).Body.String()
	waiting, answered = segmentCounts(body)
	if waiting != "1" || answered != "1" {
		t.Errorf("after answering: %s waiting / %s answered, want 1 / 1", waiting, answered)
	}
	if strings.Contains(body, "What kind of cars did he have?") {
		t.Error("an answered question should not be in the waiting view")
	}

	// It is in the answered view, marked done.
	body = h.get("/questions?show=answered", cookie).Body.String()
	if !strings.Contains(body, "What kind of cars did he have?") {
		t.Error("the answered question should be in the answered view")
	}
	if !strings.Contains(body, "qrow-done") {
		t.Error("answered rows should still be marked done")
	}
	if strings.Contains(body, "What were his favorite meals?") {
		t.Error("an unanswered question should not be in the answered view")
	}
}

// Opening the page shows your own questions; the other contributors are offered
// as a way to go and read theirs.
func TestQuestionListDefaultsToTheViewerAndOffersOthers(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")

	body := h.get("/questions", dad).Body.String()
	if !strings.Contains(body, `href="/f/home/questions?asked_of=Dad"`) {
		t.Error("expected Dad in the people list")
	}
	if !strings.Contains(body, `href="/f/home/questions?asked_of=Mom"`) {
		t.Error("expected Mom offered as somewhere else to look")
	}
	// Contributors are not offered "everyone"; only an admin is.
	if strings.Contains(body, "asked_of=everyone") {
		t.Error("a contributor should not be offered everyone's questions")
	}

	chris := h.signIn("chris@example.com")
	if !strings.Contains(h.get("/questions", chris).Body.String(), "asked_of=everyone") {
		t.Error("an admin should be able to see everyone's questions")
	}
}

func TestQuestionListFiltersByPersonAndSubject(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	mom := h.get("/questions?asked_of=Mom", cookie).Body.String()
	if !strings.Contains(mom, "How did they meet?") {
		t.Error("Mom's question missing from her filter")
	}
	if strings.Contains(mom, "What kind of cars did he have?") {
		t.Error("Dad's question leaked into Mom's filter")
	}

	bySubject := h.get("/questions?subject=peter-samuel-hale&asked_of=Dad", cookie).Body.String()
	if !strings.Contains(bySubject, "What kind of cars did he have?") {
		t.Error("subject filter dropped a matching question")
	}
	if strings.Contains(h.get("/questions?subject=no-such-subject&asked_of=Dad", cookie).Body.String(), "qrow-body") {
		t.Error("an unknown subject should match nothing")
	}
}

// The three-tier answer model: the intended person's answer is primary, everyone
// else lands under Others, and any answer can carry a reply thread.
func TestPrimaryAnswerOthersSectionAndReplies(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	mom := h.signIn("mom@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	h.post("/questions/"+id+"/answer", url.Values{"body": {"A Studebaker."}}, dad)
	h.post("/questions/"+id+"/answer", url.Values{"body": {"He loved that car."}}, mom)

	body := h.get("/questions/"+id, dad).Body.String()
	if !strings.Contains(body, "panel-primary") {
		t.Error("the intended person's answer should be marked primary")
	}
	if !strings.Contains(body, "Also remembered by") {
		t.Error("expected a divider separating other people's answers")
	}
	if strings.Index(body, "A Studebaker.") > strings.Index(body, "He loved that car.") {
		t.Error("the primary answer must render before Others")
	}

	entry, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	rec := h.post("/entries/"+strconv.FormatInt(entry.ID, 10)+"/replies",
		url.Values{"body": {"Was that 1954?"}, "return_to": {"/questions/" + id}}, mom)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reply status = %d", rec.Code)
	}

	body = h.get("/questions/"+id, dad).Body.String()
	if !strings.Contains(body, "Was that 1954?") {
		t.Error("reply did not render")
	}
	if !strings.Contains(body, `reply-by">Mom`) {
		t.Error("reply should be attributed to its author")
	}
}

// Anyone may answer any question — that is the whole point of the Others section.
func TestAnyoneMayAnswerAnyQuestionFromTheDetailPage(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")

	// Dad answering one of Mom's questions is allowed here, unlike on the card
	// stack, where it would be jumping into her queue.
	rec := h.post("/questions/"+strconv.FormatInt(h.momQuestion, 10)+"/answer",
		url.Values{"body": {"I remember how they met."}}, dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	// It must not count as Mom having answered.
	p, _ := h.store.Progress(h.ctx, h.momID)
	if p.Answered != 0 {
		t.Errorf("Mom's answered count = %d, want 0", p.Answered)
	}
	body := h.get("/questions/"+strconv.FormatInt(h.momQuestion, 10), dad).Body.String()
	if !strings.Contains(body, "hasn&rsquo;t answered this one yet") {
		t.Error("the question should still read as unanswered by Mom")
	}
}

func TestStoriesRoundTrip(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	mom := h.signIn("mom@example.com")

	rec := h.post("/stories", url.Values{
		"title":   {"The drive back from Chicago"},
		"body":    {"Nobody asked, but I keep thinking about it."},
		"subject": {"peter-samuel-hale"},
	}, dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create story status = %d", rec.Code)
	}

	body := h.get("/stories", dad).Body.String()
	for _, want := range []string{"The drive back from Chicago", "Peter Samuel Hale", "/delete"} {
		if !strings.Contains(body, want) {
			t.Errorf("stories page missing %q", want)
		}
	}

	stories, err := h.store.ListStories(h.ctx, h.dadID)
	if err != nil || len(stories) != 1 {
		t.Fatalf("ListStories = %v, %v", stories, err)
	}
	story := stories[0]

	// Mom can reply but must not see a delete control for someone else's story.
	if got := h.get("/stories", mom).Body.String(); strings.Contains(got, "/delete") {
		t.Error("delete offered on another person's story")
	}
	h.post("/entries/"+strconv.FormatInt(story.ID, 10)+"/replies",
		url.Values{"body": {"Tell me more."}, "return_to": {"/stories"}}, mom)
	if !strings.Contains(h.get("/stories", mom).Body.String(), "Tell me more.") {
		t.Error("reply to a story did not render")
	}

	// Only the author may delete.
	sid := strconv.FormatInt(story.ID, 10)
	if rec := h.post("/stories/"+sid+"/delete", url.Values{}, mom); rec.Code != http.StatusForbidden {
		t.Errorf("other person deleting: status = %d, want 403", rec.Code)
	}
	if rec := h.post("/stories/"+sid+"/delete", url.Values{}, dad); rec.Code != http.StatusSeeOther {
		t.Errorf("author deleting: status = %d, want 303", rec.Code)
	}
	if left, _ := h.store.ListStories(h.ctx, h.dadID); len(left) != 0 {
		t.Errorf("story survived deletion: %v", left)
	}
}

func TestStoryAndReplyRejectEmptyBodies(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")

	if rec := h.post("/stories", url.Values{"title": {"x"}, "body": {"   "}}, dad); rec.Code != http.StatusBadRequest {
		t.Errorf("empty story: status = %d, want 400", rec.Code)
	}
	if rec := h.post("/stories",
		url.Values{"body": {"x"}, "subject": {"nope"}}, dad); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown subject: status = %d, want 400", rec.Code)
	}
	if rec := h.post("/entries/999999/replies", url.Values{"body": {"hi"}}, dad); rec.Code != http.StatusNotFound {
		t.Errorf("reply to missing entry: status = %d, want 404", rec.Code)
	}
}

// --- phase 3: photos ----------------------------------------------------

// fakeStorage stands in for Supabase Storage so uploads are exercised without a
// service key or a network.
type fakeStorage struct {
	objects  map[string][]byte
	upserts  []string
	deleted  []string
	failNext bool
}

func newFakeStorage(t *testing.T, h *harness) *fakeStorage {
	t.Helper()
	fs := &fakeStorage{objects: map[string][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fs.failNext {
			fs.failNext = false
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"boom"}`)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/object/sign/"):
			io.WriteString(w, `{"signedURL":"/object/sign/x?token=fake"}`)
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			fs.objects[r.URL.Path] = body
			fs.upserts = append(fs.upserts, r.Header.Get("x-upsert"))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			fs.deleted = append(fs.deleted, r.URL.Path)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)

	h.server.Storage = storage.New(srv.URL, "fake-service-key")
	return fs
}

// A 1x1 PNG, so http.DetectContentType sees a real image.
var tinyPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0,
	0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0d, 0x0a, 0x2d, 0xb4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

func (h *harness) uploadPhoto(entryID int64, filename, declaredType string, content []byte, caption string, cookie *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	head := make(textproto.MIMEHeader)
	head.Set("Content-Disposition", `form-data; name="photo"; filename="`+filename+`"`)
	head.Set("Content-Type", declaredType)
	part, err := mw.CreatePart(head)
	if err != nil {
		h.t.Fatalf("CreatePart: %v", err)
	}
	part.Write(content)

	if caption != "" {
		mw.WriteField("caption", caption)
	}
	mw.WriteField("return_to", "/stories")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost,
		h.inFamily("/entries/"+strconv.FormatInt(entryID, 10)+"/photos"), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	return h.do(req)
}

func (h *harness) makeStory(cookie *http.Cookie, title string) store.Story {
	h.t.Helper()
	h.post("/stories", url.Values{"title": {title}, "body": {"A memory."}}, cookie)
	stories, err := h.store.ListStories(h.ctx, h.dadID)
	if err != nil || len(stories) == 0 {
		h.t.Fatalf("ListStories = %v, %v", stories, err)
	}
	return stories[0]
}

func TestPhotoUploadAttachesAndRenders(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	fs := newFakeStorage(t, h)
	story := h.makeStory(dad, "The house on Elm Street")

	rec := h.uploadPhoto(story.ID, "house.png", "image/png", tinyPNG, "The house on Elm Street", dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(fs.objects) != 1 {
		t.Fatalf("stored %d objects, want 1", len(fs.objects))
	}
	// Silently replacing an existing photograph would lose the original.
	if len(fs.upserts) != 1 || fs.upserts[0] != "false" {
		t.Errorf("x-upsert = %v, want [false]", fs.upserts)
	}

	attachments, err := h.store.AttachmentsForEntries(h.ctx, []int64{story.ID})
	if err != nil {
		t.Fatalf("AttachmentsForEntries: %v", err)
	}
	list := attachments[story.ID]
	if len(list) != 1 {
		t.Fatalf("got %d attachments, want 1", len(list))
	}
	if list[0].Kind != store.KindPhoto || list[0].Caption == nil || *list[0].Caption != "The house on Elm Street" {
		t.Errorf("attachment = %+v", list[0])
	}
	// The stored path must not be guessable from the entry id alone.
	if !strings.HasPrefix(list[0].StoragePath, "entries/"+strconv.FormatInt(story.ID, 10)+"/") {
		t.Errorf("StoragePath = %q", list[0].StoragePath)
	}
	if strings.HasSuffix(list[0].StoragePath, "/.png") {
		t.Error("path needs a random token, not just an extension")
	}

	body := h.get("/stories", dad).Body.String()
	if !strings.Contains(body, "token=fake") {
		t.Error("expected a signed URL in the rendered page")
	}
	if !strings.Contains(body, `alt="The house on Elm Street"`) {
		t.Error("caption should become the alt text")
	}
}

func TestPhotoUploadRefusesNonImages(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	newFakeStorage(t, h)
	story := h.makeStory(dad, "A story")

	// A PDF, and an SVG — scriptable, so not something to serve back to family.
	for _, c := range []struct {
		name, declared string
		content        []byte
	}{
		{"notes.pdf", "application/pdf", []byte("%PDF-1.4 not an image")},
		{"evil.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"sneaky.png", "application/octet-stream", []byte("MZ\x00\x00 definitely not a png")},
	} {
		rec := h.uploadPhoto(story.ID, c.name, c.declared, c.content, "", dad)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s: status = %d, want 415", c.name, rec.Code)
		}
	}
}

// Photographs belong to the writing they illustrate.
func TestPhotoUploadRefusesSomeoneElsesEntry(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	mom := h.signIn("mom@example.com")
	newFakeStorage(t, h)
	story := h.makeStory(dad, "Dad's story")

	rec := h.uploadPhoto(story.ID, "x.png", "image/png", tinyPNG, "", mom)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// If the database write fails after the object landed, the object must not be
// left orphaned in the bucket.
func TestPhotoUploadCleansUpWhenStorageFails(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	fs := newFakeStorage(t, h)
	story := h.makeStory(dad, "A story")

	fs.failNext = true
	rec := h.uploadPhoto(story.ID, "x.png", "image/png", tinyPNG, "", dad)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	// The message must reassure them their writing survived.
	if !strings.Contains(rec.Body.String(), "words are safe") {
		t.Errorf("unhelpful failure message: %s", rec.Body.String())
	}
	attachments, _ := h.store.AttachmentsForEntries(h.ctx, []int64{story.ID})
	if len(attachments[story.ID]) != 0 {
		t.Error("a failed upload must not leave an attachment row")
	}
}

func TestPhotoDeleteOnlyByUploader(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	mom := h.signIn("mom@example.com")
	fs := newFakeStorage(t, h)
	story := h.makeStory(dad, "A story")

	h.uploadPhoto(story.ID, "x.png", "image/png", tinyPNG, "", dad)
	attachments, _ := h.store.AttachmentsForEntries(h.ctx, []int64{story.ID})
	id := strconv.FormatInt(attachments[story.ID][0].ID, 10)

	if rec := h.post("/photos/"+id+"/delete", url.Values{}, mom); rec.Code != http.StatusForbidden {
		t.Errorf("other person: status = %d, want 403", rec.Code)
	}
	if rec := h.post("/photos/"+id+"/delete", url.Values{}, dad); rec.Code != http.StatusSeeOther {
		t.Errorf("uploader: status = %d, want 303", rec.Code)
	}
	attachments, _ = h.store.AttachmentsForEntries(h.ctx, []int64{story.ID})
	if len(attachments[story.ID]) != 0 {
		t.Error("attachment row survived deletion")
	}
	if len(fs.deleted) != 1 {
		t.Errorf("stored object was not removed: %v", fs.deleted)
	}
}

// Until the service key is copied off the server, the site must degrade rather
// than break.
func TestPhotosDegradeGracefullyWithoutAServiceKey(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	story := h.makeStory(dad, "A story")

	if h.server.Storage.Configured() {
		t.Fatal("harness should start with no service key")
	}
	body := h.get("/stories", dad).Body.String()
	if strings.Contains(body, "Add a photo") {
		t.Error("the upload form should be hidden until storage is configured")
	}

	rec := h.uploadPhoto(story.ID, "x.png", "image/png", tinyPNG, "", dad)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aren't switched on yet") {
		t.Errorf("expected a plain explanation, got: %s", rec.Body.String())
	}
}

// Signed photo URLs point at Supabase, so the CSP has to permit that origin for
// images while still refusing outside scripts.
func TestCSPAllowsSupabaseImagesButNotScripts(t *testing.T) {
	h := newHarness(t)
	csp := h.get("/login", nil).Header().Get("Content-Security-Policy")

	if !strings.Contains(csp, "img-src 'self' data: "+h.server.Config.SupabaseURL) {
		t.Errorf("img-src should allow Supabase, got: %s", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("script-src must stay self-only, got: %s", csp)
	}
}

// Without SUPABASE_JWT_SECRET, tokens are verified by asking Supabase. This is
// the path that lets the site deploy without copying the signing secret out of
// the Supabase instance.
func TestSignInViaSupabaseIntrospection(t *testing.T) {
	h := newHarness(t)

	// Drop the secret so the handler must fall back to introspection.
	h.server.Config.SupabaseJWTSecret = ""

	introspect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/user" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"msg":"invalid token"}`)
			return
		}
		io.WriteString(w, `{"id":"`+subjectFor("dad@example.com")+`",
		                    "email":"dad@example.com","role":"authenticated"}`)
	}))
	defer introspect.Close()
	h.server.Supabase = auth.NewSupabase(introspect.URL, "anon")

	rec := h.post("/auth/session", url.Values{"access_token": {"good-token"}}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session issued via introspection")
	}
	if rec := h.get("/cards", cookie); rec.Code != http.StatusOK {
		t.Errorf("session from introspection does not work: %d", rec.Code)
	}

	// A token Supabase refuses must not produce a session.
	if rec := h.post("/auth/session", url.Values{"access_token": {"bad-token"}}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", rec.Code)
	}
}

// --- phase 5: tree and subject pages ------------------------------------

func TestBuildTreeWalksParents(t *testing.T) {
	years := func(n int) *int { return &n }
	id := func(n int64) *int64 { return &n }
	people := []*store.TreePerson{
		{ID: 1, Given: "Peter John", Surname: "Hale", BirthYear: years(1958), FatherID: id(2), MotherID: id(3)},
		{ID: 2, Given: "Peter Samuel", Surname: "Hale", BirthYear: years(1925), FatherID: id(4)},
		{ID: 3, Given: "Margaret Irene", Surname: "Ward", BirthYear: years(1928)},
		{ID: 4, Given: "Louis Raymond", Surname: "Hale", BirthYear: years(1894)},
	}

	roots := buildTree(people, []int64{1}, 4)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if len(roots[0].Parents) != 2 {
		t.Fatalf("parents of the root = %d, want 2", len(roots[0].Parents))
	}
	// Father first, so the pedigree reads conventionally.
	if roots[0].Parents[0].Person.FullName() != "Peter Samuel Hale" {
		t.Errorf("first parent = %q", roots[0].Parents[0].Person.FullName())
	}
	if roots[0].Parents[0].Generation != 1 {
		t.Errorf("generation = %d, want 1", roots[0].Parents[0].Generation)
	}
	grandparents := roots[0].Parents[0].Parents
	if len(grandparents) != 1 || grandparents[0].Person.FullName() != "Louis Raymond Hale" {
		t.Errorf("grandparents = %+v", grandparents)
	}
	if grandparents[0].Generation != 2 {
		t.Errorf("generation = %d, want 2", grandparents[0].Generation)
	}
}

// Real genealogy data contains loops. The walk must terminate regardless.
func TestBuildTreeSurvivesCyclesAndDepthLimits(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	cyclic := []*store.TreePerson{
		{ID: 1, Given: "A", FatherID: id(2)},
		{ID: 2, Given: "B", FatherID: id(1)}, // B's father is A: a loop
	}
	roots := buildTree(cyclic, []int64{1}, 10)
	if len(roots) != 1 {
		t.Fatalf("roots = %d", len(roots))
	}
	depth := 0
	for n := roots[0]; len(n.Parents) > 0; n = n.Parents[0] {
		depth++
		if depth > 20 {
			t.Fatal("cycle was not broken")
		}
	}

	// A long chain must stop at the requested depth.
	var chain []*store.TreePerson
	for i := int64(1); i <= 10; i++ {
		p := &store.TreePerson{ID: i, Given: "P"}
		if i < 10 {
			next := i + 1
			p.FatherID = &next
		}
		chain = append(chain, p)
	}
	roots = buildTree(chain, []int64{1}, 2)
	depth = 0
	for n := roots[0]; len(n.Parents) > 0; n = n.Parents[0] {
		depth++
	}
	if depth != 2 {
		t.Errorf("depth = %d, want 2", depth)
	}
}

func TestBuildTreeIgnoresUnknownRoots(t *testing.T) {
	if got := buildTree(nil, []int64{99}, 3); len(got) != 0 {
		t.Errorf("got %d roots, want none", len(got))
	}
}

// A pedigree only reaches blood ancestors, so a subject with questions that the
// tree cannot reach must still be listed — otherwise those questions have no
// route in from this page.
func TestTreePageListsSubjectsOffThePedigree(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")
	ctx := h.ctx

	// A subject with questions and nobody in the tree, like "Further Back".
	err := h.store.InTx(ctx, func(db store.DBTX) error {
		sub, err := store.UpsertSubject(ctx, db, store.Subject{
			Slug: "further-back", Kind: "group", DisplayName: "Further Back", SortOrder: 99,
		})
		if err != nil {
			return err
		}
		_, err = store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID: sub, AskedOfUserID: h.dadID,
			Body: "Any stories about the Aldermans?", SortOrder: 50, ImportKey: "fb-1",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := h.get("/tree", cookie).Body.String()
	if !strings.Contains(body, "Also in the family") {
		t.Error("expected a section for subjects off the pedigree")
	}
	if !strings.Contains(body, "Further Back") {
		t.Error("a subject with questions but no tree position must still be reachable")
	}
}

func TestSubjectPageGathersQuestionsAndStories(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	body := h.get("/subjects/peter-samuel-hale", cookie).Body.String()
	for _, want := range []string{
		"Peter Samuel Hale",
		"What kind of cars did he have?",
		"Show these as cards", // straight into a focused card stack
	} {
		if !strings.Contains(body, want) {
			t.Errorf("subject page missing %q", want)
		}
	}
	// Mom's question is about the same subject but belongs on her list too; the
	// page is per-subject, not per-person, so it should appear.
	if !strings.Contains(body, "How did they meet?") {
		t.Error("expected every question about this subject regardless of who was asked")
	}

	if rec := h.get("/subjects/no-such-person", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("unknown subject = %d, want 404", rec.Code)
	}
}

// Stories belong to a person rather than to a top-level list, and they appear
// next to that person's answered questions.
func TestStoriesLiveOnThePersonsPage(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")

	page := h.get("/subjects/peter-samuel-hale", dad).Body.String()
	if !strings.Contains(page, "Add a story about") {
		t.Error("expected a way to add a story from the person's page")
	}
	if !strings.Contains(page, `name="subject" value="peter-samuel-hale"`) {
		t.Error("the story form should already be about this person")
	}

	rec := h.post("/stories", url.Values{
		"title":     {"The drive back from Chicago"},
		"body":      {"He drove it through a blizzard."},
		"subject":   {"peter-samuel-hale"},
		"return_to": {"/subjects/peter-samuel-hale"},
	}, dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rec.Code)
	}
	// Saving from a person's page returns there, anchored on the new story rather
	// than at the top, and not to a stories list.
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/subjects/peter-samuel-hale#entry-") {
		t.Errorf("Location = %q, want the person's page anchored on the story", loc)
	}

	page = h.get("/subjects/peter-samuel-hale", dad).Body.String()
	if !strings.Contains(page, "The drive back from Chicago") {
		t.Error("story did not appear on the person's page")
	}
	if !strings.Contains(page, "What&rsquo;s been said") {
		t.Error("stories should sit under the same heading as answered questions")
	}
}

// The nav is down to three tabs, and Stories is no longer one of them.
func TestNavIsThreeTabsAndMarksTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	body := h.get("/cards", cookie).Body.String()
	for _, want := range []string{">Cards<", ">Questions<", ">Tree<"} {
		if !strings.Contains(body, want) {
			t.Errorf("nav missing %q", want)
		}
	}
	if strings.Contains(body, `class="tab" href="/f/home/stories"`) {
		t.Error("Stories should no longer be a top-level tab")
	}
	if !strings.Contains(body, `href="/f/home/cards" aria-current="page"`) {
		t.Error("the current tab should be marked for screen readers")
	}
}

// The path from "tell me about Grandpa Louis" to actually answering.
func TestFocusSubjectSwitchesTheCardStack(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	rec := h.post("/subjects/peter-samuel-hale/focus", url.Values{}, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/f/home/cards" {
		t.Errorf("Location = %q, want /f/home/cards", loc)
	}

	m, err := h.store.MembershipOf(h.ctx, h.familyID, h.dadID)
	if err != nil {
		t.Fatalf("MembershipOf: %v", err)
	}
	if m.QueueMode != store.QueueOneSubject || m.QueueFocusSubjectID == nil {
		t.Errorf("queue not focused: mode=%q focus=%v", m.QueueMode, m.QueueFocusSubjectID)
	}

	if rec := h.post("/subjects/nope/focus", url.Values{}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("unknown subject = %d, want 404", rec.Code)
	}
}

func TestLifespanFormatting(t *testing.T) {
	y := func(n int) *int { return &n }
	cases := []struct {
		p    store.TreePerson
		want string
	}{
		{store.TreePerson{BirthYear: y(1894), DeathYear: y(1972)}, "1894–1972"},
		{store.TreePerson{BirthYear: y(1958)}, "b. 1958"},
		{store.TreePerson{DeathYear: y(1920)}, "d. 1920"},
		{store.TreePerson{}, ""},
	}
	for _, c := range cases {
		if got := c.p.Lifespan(); got != c.want {
			t.Errorf("Lifespan() = %q, want %q", got, c.want)
		}
	}
}

// Switching the person must not carry a subject filter across. Dad is asked
// nothing about Mom's side of the family, so keeping the filter emptied the page.
func TestSwitchingPersonDropsTheSubjectFilter(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")

	body := h.get("/questions?subject=peter-samuel-hale", dad).Body.String()
	if strings.Contains(body, `href="/f/home/questions?asked_of=Mom&subject=`) {
		t.Error("a person link must not carry the current subject filter")
	}
	if !strings.Contains(body, `href="/f/home/questions?asked_of=Mom"`) {
		t.Error("expected a plain link to Mom's questions")
	}
}

// An empty filter combination is not an achievement, and must not be reported as
// one. It also needs a way back out.
func TestEmptyFilterExplainsItselfRatherThanCongratulating(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	ctx := h.ctx

	// A subject Dad has no questions about.
	err := h.store.InTx(ctx, func(db store.DBTX) error {
		sub, err := store.UpsertSubject(ctx, db, store.Subject{
			Slug: "someone-else", Kind: "individual", DisplayName: "Someone Else", SortOrder: 50,
		})
		if err != nil {
			return err
		}
		_, err = store.UpsertImportedQuestion(ctx, db, store.ImportedQuestion{
			SubjectID: sub, AskedOfUserID: h.momID,
			Body: "Only Mom is asked this.", SortOrder: 60, ImportKey: "mom-only",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := h.get("/questions?asked_of=Dad&subject=someone-else", dad).Body.String()
	if strings.Contains(body, "real achievement") {
		t.Error("an empty filter must not be reported as having answered everything")
	}
	if !strings.Contains(body, "No questions match") {
		t.Error("expected an explanation of why the list is empty")
	}
	if !strings.Contains(body, `href="/f/home/questions?asked_of=Dad"`) {
		t.Error("expected a way back to all of Dad's questions")
	}

	// A filter that does match still shows its questions.
	body = h.get("/questions?asked_of=Dad&subject=peter-samuel-hale", dad).Body.String()
	if strings.Contains(body, "No questions match") {
		t.Error("a filter with questions should not report an empty result")
	}
	if !strings.Contains(body, "What kind of cars did he have?") {
		t.Error("expected the matching questions to be listed")
	}
}

// "Mom hasn't answered this one yet" is worth telling somebody else, not Mom.
// To her it refers to her in the third person, and the form below already says
// the question is hers.
func TestUnansweredNoticeIsHiddenFromThePersonAsked(t *testing.T) {
	h := newHarness(t)
	dadQ := strconv.FormatInt(h.dadQuestion, 10)

	// Dad looking at his own unanswered question.
	dad := h.signIn("dad@example.com")
	own := h.get("/questions/"+dadQ, dad).Body.String()
	if strings.Contains(own, "hasn&rsquo;t answered this one yet") {
		t.Error("the person asked should not be told they have not answered")
	}
	if !strings.Contains(own, "This one was asked of you") {
		t.Error("expected the form to say the question is theirs")
	}

	// Mom looking at the same question, which is not hers.
	mom := h.signIn("mom@example.com")
	other := h.get("/questions/"+dadQ, mom).Body.String()
	if !strings.Contains(other, "Dad hasn&rsquo;t answered this one yet") {
		t.Error("somebody else should still see that Dad has not answered")
	}

	// Once answered, nobody sees the notice.
	h.post("/questions/"+dadQ+"/answer", url.Values{"body": {"A Studebaker."}}, dad)
	after := h.get("/questions/"+dadQ, mom).Body.String()
	if strings.Contains(after, "hasn&rsquo;t answered this one yet") {
		t.Error("the notice should go once there is an answer")
	}
}

// Replying used to redirect, which reloaded the page and threw the reader back to
// the top -- losing their place in a long thread every time.
func TestReplyingSwapsInPlaceInsteadOfReloading(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	mom := h.signIn("mom@example.com")
	id := strconv.FormatInt(h.dadQuestion, 10)

	h.post("/questions/"+id+"/answer", url.Values{"body": {"A Studebaker."}}, dad)
	entry, err := h.store.AnswerFor(h.ctx, h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	entryID := strconv.FormatInt(entry.ID, 10)

	// An htmx reply comes back as the answers fragment, so the page never moves.
	req := httptest.NewRequest(http.MethodPost, h.inFamily("/entries/"+entryID+"/replies"),
		strings.NewReader(url.Values{"body": {"Was that 1954?"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(mom)
	rec := h.do(req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a fragment", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Was that 1954?") {
		t.Error("the swapped fragment should already contain the new reply")
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Error("expected a fragment, not a whole page")
	}
	if !strings.Contains(body, `id="answers"`) {
		t.Error("the fragment must be the answers region so htmx can swap it")
	}

	// Without htmx it still works, landing on the entry rather than the top.
	plain := h.post("/entries/"+entryID+"/replies",
		url.Values{"body": {"And the Buick?"}, "return_to": {"/questions/" + id}}, mom)
	if plain.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", plain.Code)
	}
	if loc := plain.Header().Get("Location"); !strings.HasSuffix(loc, "#entry-"+entryID) {
		t.Errorf("Location = %q, want an anchor on the entry", loc)
	}
}

// Photo uploads and new stories also redirected to the top of the page. They now
// land on the entry, and return_to cannot be used to bounce somebody off-site.
func TestRedirectsLandOnTheEntryAndStayOnSite(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	newFakeStorage(t, h)

	// A new story anchors on itself.
	rec := h.post("/stories", url.Values{
		"title": {"The drive back"}, "body": {"Through a blizzard."},
		"return_to": {"/subjects/peter-samuel-hale"},
	}, dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create story status = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/subjects/peter-samuel-hale#entry-") {
		t.Errorf("Location = %q, want the person's page anchored on the new story", loc)
	}

	stories, err := h.store.ListStories(h.ctx, h.dadID)
	if err != nil || len(stories) == 0 {
		t.Fatalf("ListStories = %v, %v", stories, err)
	}
	story := stories[0]

	// A photo anchors on the entry it illustrates.
	up := h.uploadPhoto(story.ID, "x.png", "image/png", tinyPNG, "", dad)
	if up.Code != http.StatusSeeOther {
		t.Fatalf("upload status = %d", up.Code)
	}
	want := "#entry-" + strconv.FormatInt(story.ID, 10)
	if loc := up.Header().Get("Location"); !strings.HasSuffix(loc, want) {
		t.Errorf("Location = %q, want it to end %q", loc, want)
	}

	// An off-site return_to is refused rather than followed.
	for _, evil := range []string{"https://example.com/phish", "//example.com/phish"} {
		rec := h.post("/stories", url.Values{
			"body": {"x"}, "return_to": {evil},
		}, dad)
		loc := rec.Header().Get("Location")
		if strings.Contains(loc, "example.com") {
			t.Errorf("return_to %q was followed off-site: %q", evil, loc)
		}
	}
}

// Anyone can ask anyone: Dad adds one for himself when he remembers something
// worth recording, and Chris asks his father what the prompts file never thought
// to. Either way it joins that person's cards.
func TestAskingAQuestion(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	ctx := h.ctx

	page := h.get("/subjects/peter-samuel-hale", dad).Body.String()
	if !strings.Contains(page, "Ask something about") {
		t.Error("expected a way to ask a question from the person's page")
	}
	if !strings.Contains(page, `name="asked_of"`) {
		t.Error("expected a choice of who should answer")
	}

	// Dad asks himself something.
	rec := h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"asked_of": {"Dad"}, "body": {"What did his workshop smell like?"},
	}, dad)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/f/home/questions/") {
		t.Errorf("Location = %q, want the new question", loc)
	}

	// It is in Dad's waiting list, and in his card stack.
	if !strings.Contains(h.get("/questions", dad).Body.String(), "workshop smell like") {
		t.Error("the new question should appear in the asking person's list")
	}
	u, _ := h.store.UserByID(ctx, h.dadID)
	cards, err := h.store.NextCards(ctx, u, 200)
	if err != nil {
		t.Fatalf("NextCards: %v", err)
	}
	var found bool
	for _, c := range cards {
		if strings.Contains(c.Body, "workshop smell like") {
			found = true
		}
	}
	if !found {
		t.Error("the new question should join the card stack")
	}

	// Chris asks Dad something. It lands on Dad, not on Chris.
	chris := h.signIn("chris@example.com")
	h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"asked_of": {"Dad"}, "body": {"Did he ever talk about the war?"},
	}, chris)

	dadCount, _ := h.store.CountQuestionsFor(ctx, h.dadID)
	if dadCount != 4 { // two seeded plus the two just added
		t.Errorf("Dad has %d questions, want 4", dadCount)
	}

	// Nonsense is refused rather than stored.
	for name, form := range map[string]url.Values{
		"empty body":     {"asked_of": {"Dad"}, "body": {"   "}},
		"unknown person": {"asked_of": {"Nobody"}, "body": {"x"}},
		"missing person": {"body": {"x"}},
	} {
		rec := h.post("/subjects/peter-samuel-hale/questions", form, dad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if rec := h.post("/subjects/nope/questions",
		url.Values{"asked_of": {"Dad"}, "body": {"x"}}, dad); rec.Code != http.StatusNotFound {
		t.Errorf("unknown subject: status = %d, want 404", rec.Code)
	}
}

// A question written on the site must survive a re-import, which archives
// imported questions no longer in the markdown.
func TestUserQuestionsSurviveAReimport(t *testing.T) {
	h := newHarness(t)
	dad := h.signIn("dad@example.com")
	ctx := h.ctx

	h.post("/subjects/peter-samuel-hale/questions", url.Values{
		"asked_of": {"Dad"}, "body": {"Written on the site, not imported."},
	}, dad)

	// Stand in for a re-import that no longer mentions any of the seeded keys.
	err := h.store.InTx(ctx, func(db store.DBTX) error {
		_, err := store.ArchiveImportedQuestionsNotIn(ctx, db, []string{"nothing-matches"})
		return err
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	if !strings.Contains(h.get("/questions", dad).Body.String(), "Written on the site") {
		t.Error("a question written on the site must not be archived by a re-import")
	}
}
