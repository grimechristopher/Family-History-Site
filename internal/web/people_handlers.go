package web

import (
	"errors"
	"net/http"
	"net/url"
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
		// Re-rendered for the line the form named, not the first one they belong to.
		r.URL.RawQuery = "family=" + url.QueryEscape(fam.Slug)
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
	// Back to the line they were just added to, or the page would show a different
	// one and look as though nothing had happened.
	http.Redirect(w, r, "/people?family="+fam.Slug+"&added="+url.QueryEscape(name), http.StatusSeeOther)
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
	data.ShownFamilyName = shown.DisplayName

	members, err := s.Store.Members(r.Context(), shown.ID)
	if err != nil {
		return data, err
	}
	data.Members2 = members

	// Only people who are not already claimed, so the picker cannot offer somebody
	// who is already somebody else.
	people, err := s.Store.UnclaimedTreePeople(r.Context(), shown.ID)
	if err != nil {
		return data, err
	}
	data.TreePeople = people
	data.Added = r.URL.Query().Get("added")
	if note := r.URL.Query().Get("note"); note != "" {
		data.Added = note
	}
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

// handleRemovePerson takes somebody out of a family. What they wrote stays.
func (s *Server) handleRemovePerson(w http.ResponseWriter, r *http.Request) {
	fam, err := s.familyFromForm(r)
	if err != nil {
		http.Error(w, "Pick which family to remove them from.", http.StatusBadRequest)
		return
	}
	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Error(w, "that wasn't somebody", http.StatusBadRequest)
		return
	}

	// Nobody removes themselves. It is almost always a misclick, and an admin who
	// did it to their only family would be locked out of the thing they run.
	if userID == auth.User(r.Context()).ID {
		http.Error(w, "You can't remove yourself.", http.StatusBadRequest)
		return
	}

	member, err := s.Store.Member(r.Context(), fam.ID, userID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	if err := s.Store.RemoveMember(r.Context(), fam.ID, userID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Log.Info("removed from family", "family", fam.Slug, "who", member.DisplayName,
		"kept", member.Written, "by", auth.User(r.Context()).DisplayName)

	note := member.DisplayName + " has been removed."
	if member.Written > 0 {
		note += " What they wrote is still here."
	}
	http.Redirect(w, r, "/people?family="+url.QueryEscape(fam.Slug)+
		"&note="+url.QueryEscape(note), http.StatusSeeOther)
}

// handleChangeEmail moves the address somebody signs in with.
//
// Both this site and Supabase have to agree, or the magic link is sent to an
// address that cannot receive it, or to nobody at all.
func (s *Server) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	fam, err := s.familyFromForm(r)
	if err != nil {
		http.Error(w, "Pick which family they're in.", http.StatusBadRequest)
		return
	}
	userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil {
		http.Error(w, "that wasn't somebody", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

	fail := func(message string) {
		r.URL.RawQuery = "family=" + url.QueryEscape(fam.Slug)
		data, dataErr := s.peoplePageData(r, "Who's here")
		if dataErr != nil {
			s.serverError(w, r, dataErr)
			return
		}
		data.Error = message
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "people", data)
	}
	if email == "" || !strings.Contains(email, "@") {
		fail("That doesn't look like an email address.")
		return
	}

	// Membership of a family this person is in is the authorisation: it is what
	// stops somebody changing the sign-in address of a stranger in another line.
	member, err := s.Store.Member(r.Context(), fam.ID, userID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if member.Email == email {
		http.Redirect(w, r, "/people?family="+url.QueryEscape(fam.Slug), http.StatusSeeOther)
		return
	}

	if err := s.Store.SetMemberEmail(r.Context(), userID, email); err != nil {
		s.serverError(w, r, err)
		return
	}

	// Supabase has to know the address before it will send anything to it. If this
	// fails the change is still saved and correct here, so say which half worked
	// rather than pretending either way.
	note := member.DisplayName + " now signs in with " + email + "."
	if err := s.Supabase.EnsureAccount(r.Context(), email, s.Config.SupabaseServiceKey); err != nil {
		s.Log.Error("could not create the supabase account", "email", email, "err", err)
		note = member.DisplayName + "'s address is changed here, but their sign-in " +
			"could not be set up. Tell Chris before asking them to log in."
	}
	s.Log.Info("changed sign-in address", "family", fam.Slug, "who", member.DisplayName,
		"by", auth.User(r.Context()).DisplayName)
	http.Redirect(w, r, "/people?family="+url.QueryEscape(fam.Slug)+
		"&note="+url.QueryEscape(note), http.StatusSeeOther)
}
