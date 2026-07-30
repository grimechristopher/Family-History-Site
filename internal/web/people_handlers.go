package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// The people page is how a family grows. Any member may add somebody, because a
// family deciding who is in it is not an administrative act -- it is the whole
// point.
//
// There is no invitation to accept. Being in the family is the authorisation:
// once an address is here, that person signs in with the ordinary magic link and
// is simply in. Somebody tells them to go and log in, and it works.

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	data, err := s.peoplePageData(r, "Who's here")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, "people", data)
}

// handleAddPerson puts somebody in this family: an identity, a membership, and a
// Supabase account so the first magic link they ask for actually arrives.
func (s *Server) handleAddPerson(w http.ResponseWriter, r *http.Request) {
	fam, err := s.familyFromForm(r)
	if err != nil {
		http.Error(w, "Pick which family to add them to.", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	name := strings.TrimSpace(r.FormValue("display_name"))

	fail := func(message string) {
		data, err := s.peoplePageData(r, "Who's here")
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Error = message
		data.Email = email
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "people", data)
	}

	if email == "" || !strings.Contains(email, "@") {
		fail("That doesn't look like an email address.")
		return
	}
	if name == "" {
		// The name is what everyone sees against their answers, so it is not
		// optional. Defaulting to the address would put a stranger's email on the
		// page next to their memories.
		fail("Give them a name — it's what shows up next to their answers.")
		return
	}

	// Which person in the tree they are, so relationship labels and their own
	// questions can find them. Optional: an in-law may be in the family without
	// being in the tree.
	var personID *int64
	if raw := r.FormValue("person_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail("That wasn't a person on the tree.")
			return
		}
		personID = &id
	}

	userID, err := s.Store.UpsertUserIn(r.Context(), email, name)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	existing, err := s.Store.MembershipOf(r.Context(), fam.ID, userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}
	if existing != nil {
		fail(name + " is already here.")
		return
	}

	if err := s.Store.AddMember(r.Context(), fam.ID, userID, store.RoleContributor); err != nil {
		s.serverError(w, r, err)
		return
	}
	if personID != nil {
		if err := s.Store.SetMemberPerson(r.Context(), fam.ID, userID, personID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}

	// Supabase has to know the address before it will email anything to it: the
	// site asks for links with should_create_user false, so without this the first
	// sign-in attempt silently sends nothing. Created already confirmed, because
	// confirming needs an email they would have to receive first.
	if err := s.Supabase.EnsureAccount(r.Context(), email, s.Config.SupabaseServiceKey); err != nil {
		// The membership is already saved and is the part that matters. Say so
		// rather than failing the whole thing: the address can be added to Supabase
		// by hand, and everything else about this person is correct.
		s.Log.Error("could not create the supabase account", "email", email, "err", err)
		data, dataErr := s.peoplePageData(r, "Who's here")
		if dataErr != nil {
			s.serverError(w, r, dataErr)
			return
		}
		data.Error = name + " has been added, but their sign-in could not be set up. " +
			"Tell Chris before asking them to log in."
		s.render(w, r, "people", data)
		return
	}

	s.Log.Info("added to family", "family", fam.Slug, "email", email, "by", auth.User(r.Context()).DisplayName)
	http.Redirect(w, r, "/people"+"?added="+name, http.StatusSeeOther)
}

func (s *Server) peoplePageData(r *http.Request, title string) (pageData, error) {
	data := s.newPageData(r, title)
	data.Nav = "people"

	// The page is about one family: whichever is named, or the first they belong
	// to. Adding somebody has to say which line they are joining.
	families := FamiliesOf(r.Context())
	if len(families) == 0 {
		return data, nil
	}
	shown := families[0]
	if slug := r.URL.Query().Get("family"); slug != "" {
		for _, f := range families {
			if f.Slug == slug {
				shown = f
			}
		}
	}
	data.ShownFamily = shown.Slug

	members, err := s.Store.Members(r.Context(), shown.ID)
	if err != nil {
		return data, err
	}
	data.Members2 = members

	// Only people who are not already claimed, so the picker cannot offer somebody
	// who is already somebody else.
	people, err := s.Store.UnclaimedTreePeople(r.Context())
	if err != nil {
		return data, err
	}
	data.TreePeople = people
	data.Added = r.URL.Query().Get("added")
	return data, nil
}

// familyFromForm resolves the family a form names, and refuses one the person is
// not a member of. The list in the context comes from their memberships, so this
// cannot be widened by editing the form.
func (s *Server) familyFromForm(r *http.Request) (*store.Family, error) {
	slug := r.FormValue("family")
	for _, f := range FamiliesOf(r.Context()) {
		if f.Slug == slug {
			return &f, nil
		}
	}
	return nil, errors.New("not a family you belong to")
}
