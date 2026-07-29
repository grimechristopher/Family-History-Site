package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Reply struct {
	ID           int64
	EntryID      int64
	AuthorUserID int64
	AuthorName   string
	Body         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *Store) CreateReply(ctx context.Context, entryID, authorUserID int64, body string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO family.replies (entry_id, author_user_id, body)
		VALUES ($1, $2, $3) RETURNING id`, entryID, authorUserID, body).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create reply on entry %d: %w", entryID, err)
	}
	return id, nil
}

// RepliesForEntries fetches replies for many entries at once, so rendering a
// question with several answers does not fan out into a query per answer.
func (s *Store) RepliesForEntries(ctx context.Context, entryIDs []int64) (map[int64][]Reply, error) {
	out := map[int64][]Reply{}
	if len(entryIDs) == 0 {
		return out, nil
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT r.id, r.entry_id, r.author_user_id, u.display_name, r.body,
		       r.created_at, r.updated_at
		FROM family.replies r
		JOIN family.users u ON u.id = r.author_user_id
		WHERE r.entry_id = ANY($1::bigint[])
		ORDER BY r.created_at`, entryIDs)
	if err != nil {
		return nil, fmt.Errorf("replies for entries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var r Reply
		if err := rows.Scan(&r.ID, &r.EntryID, &r.AuthorUserID, &r.AuthorName, &r.Body,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out[r.EntryID] = append(out[r.EntryID], r)
	}
	return out, rows.Err()
}

// ReplyAuthor is used to check that somebody is only editing their own words.
func (s *Store) ReplyAuthor(ctx context.Context, replyID int64) (int64, error) {
	var authorID int64
	err := s.Pool.QueryRow(ctx,
		`SELECT author_user_id FROM family.replies WHERE id = $1`, replyID).Scan(&authorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return authorID, err
}

func (s *Store) DeleteReply(ctx context.Context, replyID int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM family.replies WHERE id = $1`, replyID)
	return err
}

// EntryExists guards reply creation against a vanished entry.
func (s *Store) EntryExists(ctx context.Context, entryID int64) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM family.entries WHERE id = $1)`, entryID).Scan(&exists)
	return exists, err
}
