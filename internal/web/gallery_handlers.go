package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/storage"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// Photographs, as things in their own right.
//
// The point of all of this is that younger generations do not know what these
// people looked like. A 1974 gymnastics team photograph has fifteen boys in it and
// two of them are Ashley's father and uncle; unless which two is written down, the
// picture stops being evidence of anything within a generation. So a photograph
// belongs to everybody in it, appears on all their pages, and carries a point on
// each face saying who that is.

// handleGallery is everything somebody appears in.
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	subject, err := s.Store.SubjectProgressBySlug(r.Context(),
		r.PathValue("slug"), r.URL.Query().Get("family"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	photos, err := s.Store.PhotosOfSubject(r.Context(), subject.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.signPhotos(r, photos)

	// Who a picture can be said to be of: the family, not the ad-hoc names somebody
	// typed against a face in another photograph.
	people, err := s.Store.SubjectsWithProgress(r.Context(), "", subject.FamilySlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, subject.DisplayName+"’s pictures")
	data.Nav = "tree"
	data.Subject = subject
	data.Photos = photos
	data.SubjectProgress = family(people)
	s.render(w, r, "gallery", data)
}

// handlePhoto is one photograph: the picture, who is in it, and what has been
// written about it.
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	photo, err := s.Store.Photo(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	one := []store.Photo{*photo}
	s.signPhotos(r, one)
	photo = &one[0]

	written, err := s.Store.StoriesAboutPhoto(r.Context(), id, u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ids := make([]int64, 0, len(written))
	for _, st := range written {
		ids = append(ids, st.ID)
	}
	replies, err := s.Store.RepliesForEntries(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	people, err := s.Store.SubjectsWithProgress(r.Context(), "", photo.FamilySlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, "A photograph")
	data.Nav = "tree"
	data.Photo = photo
	// A pin may name anybody, including a teammate somebody typed in before. Saying
	// who a picture is of is the narrower question and gets the family only.
	data.TagChoices = people
	data.SubjectProgress = family(people)
	for _, st := range written {
		data.Stories = append(data.Stories, storyView{
			Story:         st,
			Replies:       replies[st.ID],
			IsMine:        st.AuthorUserID == u.ID,
			PhotosEnabled: false,
			ReturnTo:      "/photos/" + strconv.FormatInt(id, 10),
		})
	}
	s.render(w, r, "photo", data)
}

// handleAddGalleryPhoto takes an uploaded picture that belongs to no answer, and
// records who is in it.
func (s *Server) handleAddGalleryPhoto(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	if !s.Storage.Configured() {
		http.Error(w, "Photo uploads aren't switched on yet.", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxPhotoBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "That picture was too large to accept.", http.StatusRequestEntityTooLarge)
		return
	}

	// Which line it belongs to, taken from the people it is of rather than trusted
	// from the form: a photograph of Frank belongs to the Lucero line whatever the
	// request says.
	var subjectIDs []int64
	for _, raw := range r.Form["subject_id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			subjectIDs = append(subjectIDs, id)
		}
	}
	if len(subjectIDs) == 0 {
		http.Error(w, "Say who is in the picture.", http.StatusBadRequest)
		return
	}
	first, err := s.Store.SubjectByID(r.Context(), subjectIDs[0])
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "That isn't somebody in your family.", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	familyID, err := s.Store.FamilyOfSubject(r.Context(), first.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	file, header, err := r.FormFile("photo")
	if err != nil {
		http.Error(w, "No picture was chosen.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size > storage.MaxPhotoBytes {
		http.Error(w, "That picture was too large to accept.", http.StatusRequestEntityTooLarge)
		return
	}

	// Trust the bytes, not the filename.
	sniff := make([]byte, 512)
	n, _ := io.ReadFull(file, sniff)
	contentType := http.DetectContentType(sniff[:n])
	ext, err := storage.ExtensionFor(contentType)
	if err != nil {
		ext, err = storage.ExtensionFor(header.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "That file doesn't look like a photograph.", http.StatusUnsupportedMediaType)
			return
		}
		contentType = header.Header.Get("Content-Type")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		s.serverError(w, r, err)
		return
	}

	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		s.serverError(w, r, err)
		return
	}
	objectPath := storage.ObjectPath(0, hex.EncodeToString(token), ext)

	if err := s.Storage.Upload(r.Context(), s.Config.PhotoBucket, objectPath, contentType, file); err != nil {
		s.Log.Error("photo upload failed", "err", err)
		http.Error(w, "The picture couldn't be saved just now. Try again.", http.StatusBadGateway)
		return
	}

	photoID, err := s.Store.CreatePhoto(r.Context(), familyID, store.Photo{
		StoragePath: objectPath,
		Caption:     strings.TrimSpace(r.FormValue("caption")),
		MimeType:    contentType,
		SizeBytes:   header.Size,
		UploadedBy:  u.ID,
	})
	if err != nil {
		// Already in the bucket; take it back out so nothing is orphaned.
		if delErr := s.Storage.Delete(r.Context(), s.Config.PhotoBucket, objectPath); delErr != nil {
			s.Log.Error("could not clean up orphaned upload", "path", objectPath, "err", delErr)
		}
		s.serverError(w, r, err)
		return
	}

	for _, id := range subjectIDs {
		if err := s.Store.TagPhotoPerson(r.Context(), photoID, id, u.ID, nil, nil); err != nil {
			s.serverError(w, r, err)
			return
		}
	}

	s.Log.Info("photograph added", "photo", photoID, "of", len(subjectIDs), "by", u.DisplayName)
	http.Redirect(w, r, "/photos/"+strconv.FormatInt(photoID, 10), http.StatusSeeOther)
}

// handleTagPhotoPerson points somebody out in a photograph.
//
// The point arrives as a percentage of the width and height, so it stays on the
// right face at any size. Sent without one, it records that they are in the picture
// without saying where -- which is worth having on its own.
func (s *Server) handleTagPhotoPerson(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	photoID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.Store.Photo(r.Context(), photoID); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Either somebody already known, or a name for somebody who is not. Most of
	// the faces in a team photograph are teammates and neighbours rather than
	// family, and recording who they were is much of the point.
	var subjectID int64
	if raw := r.FormValue("subject_id"); raw != "" {
		subjectID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "Choose who that is.", http.StatusBadRequest)
			return
		}
	} else if name := strings.TrimSpace(r.FormValue("new_name")); name != "" {
		photo, err := s.Store.Photo(r.Context(), photoID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		familyID, err := s.Store.FamilyOfPhoto(r.Context(), photo.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		subjectID, err = s.Store.CreatePhotoSubject(r.Context(), familyID, name)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	} else {
		http.Error(w, "Choose who that is, or type their name.", http.StatusBadRequest)
		return
	}

	var x, y *float64
	if rx, ry := r.FormValue("x"), r.FormValue("y"); rx != "" && ry != "" {
		fx, errX := strconv.ParseFloat(rx, 64)
		fy, errY := strconv.ParseFloat(ry, 64)
		if errX != nil || errY != nil || fx < 0 || fx > 100 || fy < 0 || fy > 100 {
			http.Error(w, "that point isn't on the picture", http.StatusBadRequest)
			return
		}
		x, y = &fx, &fy
	}

	if err := s.Store.TagPhotoPerson(r.Context(), photoID, subjectID, u.ID, x, y); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/photos/"+strconv.FormatInt(photoID, 10), http.StatusSeeOther)
}

// handleUntagPhotoPerson takes somebody out of a photograph.
func (s *Server) handleUntagPhotoPerson(w http.ResponseWriter, r *http.Request) {
	photoID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	subjectID, err := strconv.ParseInt(r.FormValue("subject_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad person", http.StatusBadRequest)
		return
	}
	if err := s.Store.UntagPhotoPerson(r.Context(), photoID, subjectID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/photos/"+strconv.FormatInt(photoID, 10), http.StatusSeeOther)
}

// handlePhotoStory records something somebody wrote about a photograph. Replies to
// it are ordinary replies, so nothing else is needed for a conversation.
func (s *Server) handlePhotoStory(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	photoID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "There's nothing written to save.", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.AddPhotoStory(r.Context(), photoID, u.ID,
		strings.TrimSpace(r.FormValue("title")), body); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/photos/"+strconv.FormatInt(photoID, 10), http.StatusSeeOther)
}

// signPhotos fills in a short-lived URL for each picture. The bucket is private, so
// without one there is nothing to show.
func (s *Server) signPhotos(r *http.Request, photos []store.Photo) {
	if !s.Storage.Configured() {
		return
	}
	for i := range photos {
		url, err := s.Storage.SignedURL(r.Context(), s.Config.PhotoBucket,
			photos[i].StoragePath, signedURLTTL)
		if err != nil {
			s.Log.Error("could not sign photo", "path", photos[i].StoragePath, "err", err)
			continue
		}
		photos[i].SignedURL = url
	}
}

// family drops the people who exist only because somebody typed their name against
// a face. They belong on a pin -- that is what they are -- and not in a list of who
// a photograph is of, where a teammate would stand beside somebody's grandmother.
func family(all []store.SubjectProgress) []store.SubjectProgress {
	out := make([]store.SubjectProgress, 0, len(all))
	for _, s := range all {
		if s.Relation != "other" {
			out = append(out, s)
		}
	}
	return out
}
