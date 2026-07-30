package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/storage"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// signedURLTTL is short on purpose: a link that leaks stops working rather than
// exposing a family photograph indefinitely. Long enough to load a page and read
// it without the images expiring mid-scroll.
const signedURLTTL = 2 * time.Hour

// handleUploadPhoto attaches a picture to an answer or story.
func (s *Server) handleUploadPhoto(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad entry id", http.StatusBadRequest)
		return
	}

	if !s.Storage.Configured() {
		// Better a plain explanation than a confusing failure while the service
		// key is still missing.
		http.Error(w, "Photo uploads aren't switched on yet.", http.StatusServiceUnavailable)
		return
	}

	author, err := s.Store.EntryAuthor(r.Context(), entryID)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Photographs belong to the writing they illustrate.
	if author != u.ID {
		http.Error(w, "You can only add photos to your own answers and stories.", http.StatusForbidden)
		return
	}

	// Cap the request body before reading, so an oversized upload cannot exhaust
	// memory on the way in.
	r.Body = http.MaxBytesReader(w, r.Body, storage.MaxPhotoBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Error(w, "That picture was too large to accept. Your words are still saved.",
			http.StatusRequestEntityTooLarge)
		return
	}

	file, header, err := r.FormFile("photo")
	if err != nil {
		http.Error(w, "No picture was chosen.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if header.Size > storage.MaxPhotoBytes {
		http.Error(w, "That picture was too large to accept. Your words are still saved.",
			http.StatusRequestEntityTooLarge)
		return
	}

	// Trust the bytes, not the filename or the browser's claim.
	sniff := make([]byte, 512)
	n, _ := io.ReadFull(file, sniff)
	contentType := http.DetectContentType(sniff[:n])
	ext, err := storage.ExtensionFor(contentType)
	if err != nil {
		// http.DetectContentType does not know HEIC, so fall back to what the
		// browser declared and validate that instead of rejecting iPad photos.
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
	objectPath := storage.ObjectPath(entryID, hex.EncodeToString(token), ext)

	if err := s.Storage.Upload(r.Context(), s.Config.PhotoBucket, objectPath, contentType, file); err != nil {
		s.Log.Error("photo upload failed", "entry", entryID, "err", err)
		http.Error(w, "The picture couldn't be saved just now. Your words are safe — try the photo again.",
			http.StatusBadGateway)
		return
	}

	caption := r.FormValue("caption")
	var captionPtr *string
	if caption != "" {
		captionPtr = &caption
	}

	if _, err := s.Store.CreateAttachment(r.Context(), store.Attachment{
		EntryID:     entryID,
		Kind:        store.KindPhoto,
		StoragePath: objectPath,
		Caption:     captionPtr,
		MimeType:    contentType,
		SizeBytes:   header.Size,
		UploadedBy:  u.ID,
	}); err != nil {
		// The object is already in the bucket; remove it so nothing is orphaned.
		if delErr := s.Storage.Delete(r.Context(), s.Config.PhotoBucket, objectPath); delErr != nil {
			s.Log.Error("could not clean up orphaned upload", "path", objectPath, "err", delErr)
		}
		s.serverError(w, r, err)
		return
	}

	// Anchored on the entry: redirecting to the bare path reloaded the page and
	// threw the reader back to the top, losing the answer they were illustrating.
	back := returnTo(r, famPath(r.Context(), "/stories"))
	http.Redirect(w, r, back+"#entry-"+strconv.FormatInt(entryID, 10), http.StatusSeeOther)
}

func (s *Server) handleDeletePhoto(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad attachment id", http.StatusBadRequest)
		return
	}

	a, err := s.Store.Attachment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if a.UploadedBy != u.ID {
		http.Error(w, "That photo belongs to someone else.", http.StatusForbidden)
		return
	}

	// Remove the row first: an orphaned object costs a little disk, whereas a row
	// pointing at nothing shows the family a broken image.
	if err := s.Store.DeleteAttachment(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	if s.Storage.Configured() {
		if err := s.Storage.Delete(r.Context(), s.Config.PhotoBucket, a.StoragePath); err != nil {
			s.Log.Error("could not delete stored object", "path", a.StoragePath, "err", err)
		}
	}

	back := returnTo(r, famPath(r.Context(), "/stories"))
	http.Redirect(w, r, back+"#entry-"+strconv.FormatInt(a.EntryID, 10), http.StatusSeeOther)
}

// signAttachments fills in temporary read URLs. A failure to sign one picture
// leaves the rest of the page intact rather than failing the whole request.
func (s *Server) signAttachments(r *http.Request, groups map[int64][]store.Attachment) {
	if !s.Storage.Configured() {
		return
	}
	for entryID, list := range groups {
		for i := range list {
			bucket := s.Config.PhotoBucket
			if list[i].Kind == store.KindAudio {
				bucket = s.Config.AudioBucket
			}
			url, err := s.Storage.SignedURL(r.Context(), bucket, list[i].StoragePath, signedURLTTL)
			if err != nil {
				s.Log.Warn("could not sign attachment",
					"attachment", list[i].ID, "path", list[i].StoragePath, "err", err)
				continue
			}
			list[i].SignedURL = url
		}
		groups[entryID] = list
	}
}
