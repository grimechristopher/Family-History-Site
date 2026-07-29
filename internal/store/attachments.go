package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	KindPhoto = "photo"
	KindAudio = "audio"
)

type Attachment struct {
	ID           int64
	EntryID      int64
	Kind         string
	StoragePath  string
	Caption      *string
	MimeType     string
	SizeBytes    int64
	UploadedBy   int64
	UploaderName string
	CreatedAt    time.Time

	// SignedURL is filled in at render time, not stored.
	SignedURL string
}

func (s *Store) CreateAttachment(ctx context.Context, a Attachment) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO family.attachments
		  (entry_id, kind, storage_path, caption, mime_type, size_bytes, uploaded_by_user_id, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7,
		        coalesce((SELECT max(sort_order) + 1 FROM family.attachments WHERE entry_id = $1), 0))
		RETURNING id`,
		a.EntryID, a.Kind, a.StoragePath, a.Caption, a.MimeType, a.SizeBytes, a.UploadedBy).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create attachment on entry %d: %w", a.EntryID, err)
	}
	return id, nil
}

// AttachmentsForEntries fetches in one query, so a page with several answers does
// not fan out per answer.
func (s *Store) AttachmentsForEntries(ctx context.Context, entryIDs []int64) (map[int64][]Attachment, error) {
	out := map[int64][]Attachment{}
	if len(entryIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.entry_id, a.kind, a.storage_path, a.caption, a.mime_type,
		       a.size_bytes, a.uploaded_by_user_id, u.display_name, a.created_at
		FROM family.attachments a
		JOIN family.users u ON u.id = a.uploaded_by_user_id
		WHERE a.entry_id = ANY($1::bigint[])
		ORDER BY a.sort_order, a.id`, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("attachments for entries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.EntryID, &a.Kind, &a.StoragePath, &a.Caption,
			&a.MimeType, &a.SizeBytes, &a.UploadedBy, &a.UploaderName, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.EntryID] = append(out[a.EntryID], a)
	}
	return out, rows.Err()
}

func (s *Store) Attachment(ctx context.Context, id int64) (*Attachment, error) {
	var a Attachment
	err := s.Pool.QueryRow(ctx, `
		SELECT a.id, a.entry_id, a.kind, a.storage_path, a.caption, a.mime_type,
		       a.size_bytes, a.uploaded_by_user_id, u.display_name, a.created_at
		FROM family.attachments a
		JOIN family.users u ON u.id = a.uploaded_by_user_id
		WHERE a.id = $1`, id).Scan(&a.ID, &a.EntryID, &a.Kind, &a.StoragePath, &a.Caption,
		&a.MimeType, &a.SizeBytes, &a.UploadedBy, &a.UploaderName, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) DeleteAttachment(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM family.attachments WHERE id = $1`, id)
	return err
}

// EntryAuthor reports who wrote an entry, so only they may attach to it.
func (s *Store) EntryAuthor(ctx context.Context, entryID int64) (int64, error) {
	var authorID int64
	err := s.Pool.QueryRow(ctx,
		`SELECT author_user_id FROM family.entries WHERE id = $1`, entryID).Scan(&authorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return authorID, err
}
