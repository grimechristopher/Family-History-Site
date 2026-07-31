package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// handleDevLogin signs in as a contributor by name, so the site can be opened as
// Mom or Dad from a link. Registered only when DEV_LOGIN=1; see config.DevLogin.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	slug := r.PathValue("family")
	if slug == "" {
		http.Error(w, "use /dev/login/{family}/{name}: a name alone no longer says "+
			"which family you mean, because every family has a Dad", http.StatusNotFound)
		return
	}

	fam, err := s.Store.FamilyBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No family called "+slug, http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Scoped to the family: display names are only unique inside one.
	u, err := s.Store.MemberByDisplayName(r.Context(), fam.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		names, nErr := s.Store.MemberNames(r.Context(), fam.ID)
		if nErr != nil {
			s.serverError(w, r, nErr)
			return
		}
		http.Error(w, "Nobody called "+name+" in "+slug+". Try one of: "+
			strings.Join(names, ", "), http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	if err := s.Sessions.Issue(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Log.Warn("dev login used", "as", u.DisplayName, "family", slug, "user", u.ID)
	// The questions for that line. It used to be /f/{slug}/, which stopped
	// existing when the pages moved back to plain addresses -- so every one of
	// these links signed you in correctly and then landed on a 404.
	http.Redirect(w, r, "/questions?family="+url.QueryEscape(slug), http.StatusSeeOther)
}

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

	// Check the allowlist before asking Supabase for anything: a family-only site
	// should not send email to strangers.
	//
	// This used to answer "check your email" either way, so as not to reveal who is
	// on the list. That reads as discretion and behaves as a trap -- no email ever
	// arrives, and the next screen asks for a code out of that email. Somebody
	// mistyping their own address sits there waiting for a message nobody sent. For
	// a site with nine accounts, all of them relatives, saying so plainly is worth
	// more than hiding which addresses exist.
	deny := func(reason string) {
		s.Log.Warn("login refused", "email", email, "why", reason)
		denied := s.newPageData(r, "Not on the list")
		denied.Email = email
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "denied", denied)
	}

	user, err := s.Store.UserByEmail(r.Context(), email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		deny("no such address")
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	// An address can be known and still lead nowhere: an account added and then
	// removed from the only line it was in, or one created before anybody put it in
	// a family. Signing in would work and show an empty site, so it is refused here
	// with an explanation instead.
	families, err := s.Store.FamiliesOf(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(families) == 0 {
		deny("in no family")
		return
	}

	redirectTo := s.Config.BaseURL + "/auth/callback"
	err = s.Supabase.SendMagicLink(r.Context(), email, redirectTo)
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		// Pressing the button twice is the most likely way to get here, and the
		// first email is already in their inbox. Sending them back to the address
		// form would hide the code field that would let them straight in, so this
		// goes on to the same screen a successful send does.
		s.Log.Info("magic link asked for again too soon", "email", email)
		data.Sent = true
		data.Notice = "One is already on its way. Check your email — and if you asked twice, " +
			"the first one still works."
	case err != nil:
		s.Log.Error("could not send magic link", "email", email, "err", err)
		data.Error = "We couldn't send the email just now. Please try again in a minute."
	default:
		data.Sent = true
	}
	s.render(w, r, "login", data)
}

// handleCallback serves the one page that needs JavaScript: magic-link tokens
// arrive in the URL fragment, which never reaches the server.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "callback", s.newPageData(r, "Signing in"))
}

// handleSession exchanges a verified Supabase token for this site's own session.
// The token arrives from the callback page, which reads it out of the URL
// fragment.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("access_token")
	if token == "" {
		http.Error(w, "missing access token", http.StatusBadRequest)
		return
	}
	s.signIn(w, r, token)
}

// handleCodeSubmit signs someone in with the six-digit code from the email.
//
// The email offers the code as an alternative to the link, so the site has to
// accept it. It is also the more dependable of the two: the link only comes back
// here if this site's callback URL is in Supabase's allow list, whereas the code
// is typed in and exchanged server-side, which needs no allow list, no JavaScript
// and not even the same device.
func (s *Server) handleCodeSubmit(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	// People read the code off a screen and type it with whatever spacing they
	// see, and iOS likes to paste it with a trailing space.
	code := strings.Map(func(ch rune) rune {
		if ch >= '0' && ch <= '9' {
			return ch
		}
		return -1
	}, r.FormValue("code"))

	data := s.newPageData(r, "Sign in")
	data.Email = email
	data.Sent = true

	if email == "" || code == "" {
		data.Error = "Type the six-digit code from the email."
		s.render(w, r, "login", data)
		return
	}

	// Same allowlist check as the link route, for the same reason: a valid
	// Supabase login is not by itself permission to be here.
	if _, err := s.Store.UserByEmail(r.Context(), email); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("code sign-in for unknown address", "email", email)
			data.Error = "That code did not work. Ask for a new link and try again."
			s.render(w, r, "login", data)
			return
		}
		s.serverError(w, r, err)
		return
	}

	token, err := s.Supabase.VerifyEmailOTP(r.Context(), email, code)
	if err != nil {
		if errors.Is(err, auth.ErrBadCode) {
			// Wrong, used, or expired all read the same and all have one fix.
			data.Error = "That code did not work. It may have expired, or already been used. " +
				"Ask for a new link and try again."
			s.render(w, r, "login", data)
			return
		}
		s.Log.Error("could not verify sign-in code", "email", email, "err", err)
		data.Error = "We couldn't check that code just now. Please try again in a minute."
		s.render(w, r, "login", data)
		return
	}

	s.signIn(w, r, token)
}

// signIn is the one place a Supabase token becomes a session on this site,
// whether it arrived from the link or from the code.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, token string) {
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
