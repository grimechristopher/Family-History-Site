package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

func (s *Server) handleStories(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	stories, err := s.Store.ListStories(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	ids := make([]int64, 0, len(stories))
	for _, st := range stories {
		ids = append(ids, st.ID)
	}
	replies, err := s.Store.RepliesForEntries(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	photos, err := s.Store.AttachmentsForEntries(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.signAttachments(r, photos)

	subjects, err := s.Store.Subjects(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, "Stories")
	data.Subjects = subjects
	for _, st := range stories {
		data.Stories = append(data.Stories, storyView{
			Story:         st,
			Replies:       replies[st.ID],
			Photos:        photos[st.ID],
			IsMine:        st.AuthorUserID == u.ID,
			PhotosEnabled: s.Storage.Configured(),
			ReturnTo:      "/stories",
		})
	}
	s.render(w, r, "stories", data)
}

type storyView struct {
	store.Story
	Replies       []store.Reply
	Photos        []store.Attachment
	IsMine        bool
	PhotosEnabled bool
	ReturnTo      string
}

func (s *Server) handleCreateStory(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "a story needs something in it", http.StatusBadRequest)
		return
	}
	if title == "" {
		title = "Untitled"
	}

	var subjectID *int64
	if raw := r.FormValue("subject"); raw != "" {
		sub, err := s.subjectFromForm(r, raw)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown subject", http.StatusBadRequest)
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
		subjectID = &sub.ID
	}

	// A story about nobody in particular still belongs to a line. The one named on
	// the form, or the first they belong to.
	var familyID int64
	if fams := FamiliesOf(r.Context()); len(fams) > 0 {
		familyID = fams[0].ID
		if slug := r.FormValue("family"); slug != "" {
			for _, f := range fams {
				if f.Slug == slug {
					familyID = f.ID
				}
			}
		}
	}

	id, err := s.Store.CreateStory(r.Context(), u.ID, title, body, subjectID, false, familyID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Anchored on the new story, so it is the first thing seen rather than the
	// top of the page it was written from.
	back := returnTo(r, "/stories")
	http.Redirect(w, r, back+"#entry-"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handleDeleteStory(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad story id", http.StatusBadRequest)
		return
	}
	st, err := s.Store.Story(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Only your own words are yours to remove.
	if st.AuthorUserID != u.ID {
		http.Error(w, "that story belongs to someone else", http.StatusForbidden)
		return
	}
	if err := s.Store.DeleteStory(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Nothing to anchor to any more, so the page it came from is right.
	http.Redirect(w, r, returnTo(r, "/stories"), http.StatusSeeOther)
}
