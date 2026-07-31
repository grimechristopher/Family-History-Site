package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/grimechristopher/family-history-site/internal/store"
)

// SessionCookie is the cookie holding this site's own session token, which is
// independent of Supabase token refresh so the parents stay logged in.
const SessionCookie = "fhs_session"

// SessionTTL is deliberately long. Someone in their eighties logging in once on
// an iPad and never again is the whole point.
const SessionTTL = 90 * 24 * time.Hour

type ctxKey struct{}

// Sessions issues and resolves session cookies.
type Sessions struct {
	Store  *store.Store
	Secure bool // false only for local http development
}

func (s *Sessions) Issue(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := s.Store.CreateSession(r.Context(), userID, SessionTTL, r.UserAgent())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(SessionTTL),
	})
	return nil
}

func (s *Sessions) Revoke(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// User pulls the authenticated user out of a request context, or nil.
func User(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKey{}).(*store.User)
	return u
}

// WithUser attaches the signed-in person. Exported because the family middleware
// re-attaches them with this family's role and queue settings merged on.
func WithUser(ctx context.Context, u *store.User) context.Context {
	return withUser(ctx, u)
}

func withUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// Require wraps handlers that need a signed-in user, redirecting to the login
// page otherwise.
func (s *Sessions) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			// No cookie: this is somebody arriving, not somebody being turned out.
			redirectToLogin(w, r, false)
			return
		}
		u, err := s.Store.UserBySessionToken(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				http.Error(w, "something went wrong on our end", http.StatusInternalServerError)
				return
			}
			// A cookie that no longer works: they were signed in until now, so the
			// page owes them an explanation.
			redirectToLogin(w, r, true)
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), u)))
	})
}

// Optional attaches the user when there is one but never blocks the request.
func (s *Sessions) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err == nil {
			if u, err := s.Store.UserBySessionToken(r.Context(), c.Value); err == nil {
				r = r.WithContext(withUser(r.Context(), u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// redirectToLogin sends somebody to the sign-in page.
//
// expired distinguishes the two ways of arriving there, which are not the same thing
// to the person reading the page. A session that ran out deserves "you were signed
// out, what you wrote was saved". Somebody opening the site for the first time has
// not been signed out of anything and has written nothing -- telling them their work
// was saved invites them to wonder what they lost.
func redirectToLogin(w http.ResponseWriter, r *http.Request, expired bool) {
	to := "/login"
	if expired {
		to += "?expired=1"
	}
	// htmx needs an explicit instruction; a 302 would be swallowed into the
	// swap target and the login page would appear inside the card.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
