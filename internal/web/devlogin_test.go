package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The dev-login links are how the family is asked to try the site before it is
// deployed anywhere. They signed people in correctly and then sent them to
// /f/{family}/, an address that stopped existing when the pages moved back to
// plain ones -- so every link handed out ended on "404 page not found" and looked
// like the site was broken.
//
// This asserts the destination exists, rather than that the redirect happened.
func TestDevLoginLandsOnAPageThatExists(t *testing.T) {
	h := newHarness(t)

	// Built with the route on rather than skipped when it is off: a test that
	// skips is not a test, and this route is exactly the one nobody exercises
	// until they hand the link to their family.
	cfg := h.server.Config
	cfg.DevLogin = true
	srv, err := New(cfg, h.store, slog.New(slog.NewTextHandler(os.Stderr, nil)), "test")
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	routes := srv.Routes()

	call := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec
	}

	rec := call("/dev/login/home/Dad", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("dev login: status %d, body %s", rec.Code, rec.Body.String())
	}
	where := rec.Header().Get("Location")
	if strings.HasPrefix(where, "/f/") {
		t.Fatalf("sent to %s, which is not a route any more", where)
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "fhs_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("dev login issued no session")
	}

	landing := call(where, cookie)
	if landing.Code != http.StatusOK {
		t.Fatalf("landed on %s with status %d", where, landing.Code)
	}
}
