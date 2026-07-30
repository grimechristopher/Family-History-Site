package web

import (
	"net/http"

	"github.com/grimechristopher/family-history-site/internal/auth"
)

// handleRoot sends somebody to their family.
//
// Almost everybody belongs to one, and they never see this page: they land on it
// for the time it takes to redirect. The chooser exists for the few people who are
// in more than one -- somebody and their spouse, each recording their own parents.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	families, err := s.Store.FamiliesOf(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	switch len(families) {
	case 1:
		http.Redirect(w, r, "/f/"+families[0].Slug+"/", http.StatusSeeOther)
	default:
		// Nought or several. Nought happens when an invitation was revoked after
		// it was accepted, or an account exists that has not been added to
		// anything yet, and it needs to say so rather than redirect nowhere.
		data := s.newPageData(r, "Your families")
		data.Families = families
		s.render(w, r, "families", data)
	}
}
