package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if auth.User(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data := s.newPageData(r, "Sign in")
	data.Expired = r.URL.Query().Get("expired") == "1"
	s.render(w, r, "login", data)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

	data := s.newPageData(r, "Sign in")
	data.Email = email

	if email == "" || !strings.Contains(email, "@") {
		data.Error = "That doesn't look like an email address."
		s.render(w, r, "login", data)
		return
	}

	// Check the allowlist before asking Supabase for anything. A family-only site
	// should not send email to strangers, and it should not reveal who is on the
	// list either — so an unknown address gets the same "check your email"
	// response as a known one.
	_, err := s.Store.UserByEmail(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.Log.Warn("login attempt for unknown address", "email", email)
		data.Sent = true
		s.render(w, r, "login", data)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	redirectTo := s.Config.BaseURL + "/auth/callback"
	if err := s.Supabase.SendMagicLink(r.Context(), email, redirectTo); err != nil {
		s.Log.Error("could not send magic link", "email", email, "err", err)
		data.Error = "We couldn't send the email just now. Please try again in a minute."
		s.render(w, r, "login", data)
		return
	}

	data.Sent = true
	s.render(w, r, "login", data)
}

// handleCallback serves the one page that needs JavaScript: magic-link tokens
// arrive in the URL fragment, which never reaches the server.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "callback", s.newPageData(r, "Signing in"))
}

// handleSession exchanges a verified Supabase token for this site's own session.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("access_token")
	if token == "" {
		http.Error(w, "missing access token", http.StatusBadRequest)
		return
	}

	claims, err := s.verifyToken(r, token)
	if err != nil {
		s.Log.Warn("rejected supabase token", "err", err)
		http.Error(w, "That sign-in link could not be verified. Please request a new one.",
			http.StatusUnauthorized)
		return
	}

	user, err := s.Store.UserByEmail(r.Context(), claims.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// A valid Supabase login that is not on the allowlist. This happens for
		// real: the same Supabase project backs a public portfolio where anyone
		// may sign up, so it needs to read as a clear explanation rather than an
		// error.
		s.Log.Warn("verified login not on allowlist", "email", claims.Email)
		data := s.newPageData(r, "Not on the list")
		data.Email = claims.Email
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "denied", data)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	if user.SupabaseUserID == nil || *user.SupabaseUserID != claims.Subject {
		err := s.Store.BackfillSupabaseUserID(r.Context(), user.ID, claims.Subject)
		if errors.Is(err, store.ErrIdentityClaimed) {
			// Two allowlist rows pointing at one Supabase account. Nothing the
			// person signing in can do about it, so say so rather than showing
			// them a generic failure.
			s.Log.Error("supabase identity already claimed",
				"email", claims.Email, "supabase_user_id", claims.Subject)
			http.Error(w,
				"This sign-in is already linked to a different family member. Ask Chris to sort it out.",
				http.StatusConflict)
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	}

	if err := s.Sessions.Issue(w, r, user.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Log.Info("signed in", "user", user.DisplayName)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.Sessions.Revoke(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// verifyToken checks a Supabase access token by whichever means is configured.
//
// With SUPABASE_JWT_SECRET set, the signature is checked locally: no network
// call, and login keeps working even if the Supabase API is briefly unreachable.
// Without it, Supabase is asked to verify its own token, so the signing secret
// never has to leave the Supabase instance at all.
func (s *Server) verifyToken(r *http.Request, token string) (auth.Claims, error) {
	if s.Config.SupabaseJWTSecret != "" {
		return auth.VerifySupabaseJWT(token, s.Config.SupabaseJWTSecret,
			s.Config.SupabaseJWTIssuer, time.Now())
	}
	return s.Supabase.UserFromToken(r.Context(), token)
}
