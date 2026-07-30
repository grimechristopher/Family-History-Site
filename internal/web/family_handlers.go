package web

import (
	"net/http"
	"time"

	"github.com/grimechristopher/family-history-site/internal/auth"
)

// lastFamilyCookie remembers which family you were last in, so signing in takes you
// back where you were rather than asking.
const lastFamilyCookie = "fhs_family"

// handleRoot sends you to a family. It never asks which.
//
// Being in four of them is normal here -- two parents' lines each for a married
// couple -- and a chooser standing between somebody and the page they wanted is a
// toll on every visit. You land in the one you were last in, and switch from the
// bar if you want another.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	families, err := s.Store.FamiliesOf(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(families) == 0 {
		// Signed in and in nothing: an account exists that has not been added to a
		// family. Saying so beats redirecting nowhere.
		data := s.newPageData(r, "Your families")
		s.render(w, r, "families", data)
		return
	}

	target := families[0].Slug
	if c, err := r.Cookie(lastFamilyCookie); err == nil {
		for _, f := range families {
			if f.Slug == c.Value {
				target = f.Slug
				break
			}
		}
	}
	http.Redirect(w, r, "/f/"+target+"/", http.StatusSeeOther)
}

// rememberFamily records the family being viewed, for the next visit to the root.
func rememberFamily(w http.ResponseWriter, slug string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     lastFamilyCookie,
		Value:    slug,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(365 * 24 * time.Hour),
	})
}
