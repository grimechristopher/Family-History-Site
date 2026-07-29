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
	sentEmails  []string
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
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS family CASCADE"); err != nil {
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
		var payload struct{ Email string }
		_ = json.Unmarshal(body, &payload)
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
	srv, err := New(cfg, s, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	h.server = srv
	h.handler = srv.Routes()

	// Seed two contributors, each with a question of their own.
	err = s.InTx(ctx, func(db store.DBTX) error {
		dad, err := store.UpsertUser(ctx, db, "dad@example.com", "Dad", store.RoleContributor)
		if err != nil {
			return err
		}
		mom, err := store.UpsertUser(ctx, db, "mom@example.com", "Mom", store.RoleContributor)
		if err != nil {
			return err
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

func (h *harness) post(path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return h.do(req)
}

func (h *harness) get(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
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

	req := httptest.NewRequest(http.MethodPost, "/cards/1/defer", nil)
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

	u, err := h.store.UserByEmail(context.Background(), "dad@example.com")
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
		"Peter Samuel Hale",            // the subject
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

	e, err := h.store.AnswerFor(context.Background(), h.dadQuestion, h.dadID)
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
	if _, err := h.store.AnswerFor(context.Background(), h.dadQuestion, h.dadID); err != store.ErrNotFound {
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

	e, err := h.store.AnswerFor(context.Background(), h.dadQuestion, h.dadID)
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

	e, err := h.store.AnswerFor(context.Background(), h.dadQuestion, h.dadID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	if e.IsDraft {
		t.Error("a published answer must stay published")
	}
	p, _ := h.store.Progress(context.Background(), h.dadID)
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
	u, _ := h.store.UserByID(context.Background(), h.dadID)
	if u.QueueMode != store.QueueShuffle {
		t.Errorf("QueueMode = %q, want shuffle", u.QueueMode)
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

func TestQuestionListSeparatesUnansweredFromAnswered(t *testing.T) {
	h := newHarness(t)
	cookie := h.signIn("dad@example.com")

	body := h.get("/questions", cookie).Body.String()
	if !strings.Contains(body, "3 still waiting") {
		t.Errorf("expected all three waiting, got: %s", firstLede(body))
	}

	h.post("/questions/"+strconv.FormatInt(h.dadQuestion, 10)+"/answer",
		url.Values{"body": {"A Studebaker."}}, cookie)

	body = h.get("/questions", cookie).Body.String()
	if !strings.Contains(body, "2 still waiting") || !strings.Contains(body, "1 answered") {
		t.Errorf("counts did not move, got: %s", firstLede(body))
	}
	// The answered one stays visible rather than disappearing.
	if !strings.Contains(body, "qrow-done") {
		t.Error("answered questions should still be listed, marked done")
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

	bySubject := h.get("/questions?subject=peter-samuel-hale", cookie).Body.String()
	if !strings.Contains(bySubject, "What kind of cars did he have?") {
		t.Error("subject filter dropped a matching question")
	}
	if strings.Contains(h.get("/questions?subject=no-such-subject", cookie).Body.String(), "qrow-body") {
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
	if !strings.Contains(body, "answer-primary") {
		t.Error("the intended person's answer should be marked primary")
	}
	if !strings.Contains(body, "<h2>Others</h2>") {
		t.Error("expected an Others section")
	}
	if strings.Index(body, "A Studebaker.") > strings.Index(body, "He loved that car.") {
		t.Error("the primary answer must render before Others")
	}

	entry, err := h.store.AnswerFor(context.Background(), h.dadQuestion, h.dadID)
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
	p, _ := h.store.Progress(context.Background(), h.momID)
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

	stories, err := h.store.ListStories(context.Background(), h.dadID)
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
	if left, _ := h.store.ListStories(context.Background(), h.dadID); len(left) != 0 {
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

func firstLede(body string) string {
	i := strings.Index(body, `class="lede">`)
	if i < 0 {
		return "(no lede)"
	}
	rest := body[i+13:]
	if j := strings.Index(rest, "<"); j >= 0 {
		return rest[:j]
	}
	return rest
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
		"/entries/"+strconv.FormatInt(entryID, 10)+"/photos", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	return h.do(req)
}

func (h *harness) makeStory(cookie *http.Cookie, title string) store.Story {
	h.t.Helper()
	h.post("/stories", url.Values{"title": {title}, "body": {"A memory."}}, cookie)
	stories, err := h.store.ListStories(context.Background(), h.dadID)
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

	attachments, err := h.store.AttachmentsForEntries(context.Background(), []int64{story.ID})
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
	attachments, _ := h.store.AttachmentsForEntries(context.Background(), []int64{story.ID})
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
	attachments, _ := h.store.AttachmentsForEntries(context.Background(), []int64{story.ID})
	id := strconv.FormatInt(attachments[story.ID][0].ID, 10)

	if rec := h.post("/photos/"+id+"/delete", url.Values{}, mom); rec.Code != http.StatusForbidden {
		t.Errorf("other person: status = %d, want 403", rec.Code)
	}
	if rec := h.post("/photos/"+id+"/delete", url.Values{}, dad); rec.Code != http.StatusSeeOther {
		t.Errorf("uploader: status = %d, want 303", rec.Code)
	}
	attachments, _ = h.store.AttachmentsForEntries(context.Background(), []int64{story.ID})
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
