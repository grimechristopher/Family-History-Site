package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/grimechristopher/family-history-site/internal/subjects"
	"time"

	"github.com/jackc/pgx/v5"
)

// A photograph, as something in its own right rather than as a thing hanging off
// an answer.
//
// The reason it needs to be its own thing: a picture of two brothers belongs on
// both their pages, and a picture somebody found in a box belongs on the site
// before anybody has written a word about it. Neither is possible when a photograph
// is a child of one person's answer.
type Photo struct {
	ID           int64
	StoragePath  string
	Caption      string
	MimeType     string
	SizeBytes    int64
	UploadedBy   int64
	UploaderName string
	CreatedAt    time.Time
	FamilySlug   string

	// Who is in it, and whereabouts.
	People []PhotoPerson

	// SignedURL is filled in at render time, never stored: the bucket is private
	// and a URL that worked forever would be a way round that.
	SignedURL string
}

// PhotoPerson is one person in a photograph, and where in it they are.
//
// The point is a percentage of the width and height rather than a pixel, so it
// stays on the right face at any size -- a thumbnail, full width on a phone, or a
// print. Nil means somebody said they are in the picture without saying where,
// which is worth recording on its own.
type PhotoPerson struct {
	SubjectID   int64
	SubjectSlug string
	DisplayName string
	PointX      *float64
	PointY      *float64
}

// Placed reports whether this person has been pointed out in the picture.
func (p PhotoPerson) Placed() bool { return p.PointX != nil && p.PointY != nil }

const photoColumns = `
	a.id, a.storage_path, coalesce(a.caption, ''), a.mime_type, a.size_bytes,
	a.uploaded_by_user_id, u.display_name, a.created_at, f.slug`

func scanPhoto(row pgx.Row) (*Photo, error) {
	var p Photo
	err := row.Scan(&p.ID, &p.StoragePath, &p.Caption, &p.MimeType, &p.SizeBytes,
		&p.UploadedBy, &p.UploaderName, &p.CreatedAt, &p.FamilySlug)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePhoto records an uploaded photograph that belongs to no answer.
func (s *Store) CreatePhoto(ctx context.Context, familyID int64, p Photo) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.attachments
		  (entry_id, kind, storage_path, caption, mime_type, size_bytes,
		   uploaded_by_user_id, family_id)
		VALUES (NULL, 'photo', $1, nullif($2, ''), $3, $4, $5, $6)
		RETURNING id`,
		p.StoragePath, p.Caption, p.MimeType, p.SizeBytes, p.UploadedBy, familyID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create photo: %w", err)
	}
	return id, nil
}

// Photo is one photograph with everybody in it.
func (s *Store) Photo(ctx context.Context, id int64) (*Photo, error) {
	p, err := scanPhoto(s.q(ctx).QueryRow(ctx, `
		SELECT `+photoColumns+`
		  FROM family.attachments a
		  JOIN core.users u ON u.id = a.uploaded_by_user_id
		  JOIN core.families f ON f.id = a.family_id
		 WHERE a.id = $1 AND a.kind = 'photo'`, id))
	if err != nil {
		return nil, err
	}
	people, err := s.PhotoPeople(ctx, id)
	if err != nil {
		return nil, err
	}
	p.People = people
	return p, nil
}

// PhotoPeople is who is in a photograph.
func (s *Store) PhotoPeople(ctx context.Context, photoID int64) ([]PhotoPerson, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT ps.subject_id, s.slug, s.display_name, ps.point_x, ps.point_y
		  FROM family.photo_subjects ps
		  JOIN family.subjects s ON s.id = ps.subject_id
		 WHERE ps.attachment_id = $1
		 ORDER BY ps.point_x NULLS LAST, s.display_name`, photoID)
	if err != nil {
		return nil, fmt.Errorf("photo people: %w", err)
	}
	defer rows.Close()

	var out []PhotoPerson
	for rows.Next() {
		var p PhotoPerson
		if err := rows.Scan(&p.SubjectID, &p.SubjectSlug, &p.DisplayName,
			&p.PointX, &p.PointY); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PhotosOfSubject is every photograph somebody is in, for their gallery.
func (s *Store) PhotosOfSubject(ctx context.Context, subjectID int64) ([]Photo, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT `+photoColumns+`
		  FROM family.photo_subjects ps
		  JOIN family.attachments a ON a.id = ps.attachment_id
		  JOIN core.users u ON u.id = a.uploaded_by_user_id
		  JOIN core.families f ON f.id = a.family_id
		 WHERE ps.subject_id = $1 AND a.kind = 'photo'
		 ORDER BY a.created_at DESC, a.id DESC`, subjectID)
	if err != nil {
		return nil, fmt.Errorf("photos of subject: %w", err)
	}
	defer rows.Close()

	var out []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Everybody in each one, so a thumbnail can say who is in it without a second
	// round trip per picture.
	for i := range out {
		people, err := s.PhotoPeople(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].People = people
	}
	return out, nil
}

// TagPhotoPerson says somebody is in a photograph, optionally pointing them out.
// Repeating it moves the point rather than failing, which is what correcting a
// misplaced pin is.
func (s *Store) TagPhotoPerson(ctx context.Context, photoID, subjectID, byUserID int64, x, y *float64) error {
	_, err := s.q(ctx).Exec(ctx, `
		INSERT INTO family.photo_subjects
		  (family_id, attachment_id, subject_id, point_x, point_y, added_by_user_id)
		SELECT a.family_id, a.id, $2, $3, $4, $5
		  FROM family.attachments a WHERE a.id = $1
		ON CONFLICT (attachment_id, subject_id) DO UPDATE
		   SET point_x = EXCLUDED.point_x, point_y = EXCLUDED.point_y`,
		photoID, subjectID, x, y, byUserID)
	if err != nil {
		return fmt.Errorf("tag photo person: %w", err)
	}
	return nil
}

// UntagPhotoPerson takes somebody out of a photograph.
func (s *Store) UntagPhotoPerson(ctx context.Context, photoID, subjectID int64) error {
	_, err := s.q(ctx).Exec(ctx,
		`DELETE FROM family.photo_subjects WHERE attachment_id = $1 AND subject_id = $2`,
		photoID, subjectID)
	if err != nil {
		return fmt.Errorf("untag photo person: %w", err)
	}
	return nil
}

// StoriesAboutPhoto is what people have written about a photograph. They are
// ordinary entries, so replies to them work with no changes at all.
func (s *Store) StoriesAboutPhoto(ctx context.Context, photoID, viewerID int64) ([]Story, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT `+storyColumns+storyJoins+`
		  AND e.attachment_id = $1
		  AND (e.is_draft = false OR e.author_user_id = $2)
		ORDER BY e.created_at`, photoID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("stories about photo: %w", err)
	}
	defer rows.Close()

	var out []Story
	for rows.Next() {
		st, err := scanStory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// AddPhotoStory records something somebody wrote about a photograph.
func (s *Store) AddPhotoStory(ctx context.Context, photoID, authorID int64, title, body string) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.entries
		  (attachment_id, author_user_id, title, body, is_draft, family_id)
		SELECT a.id, $2, nullif($3, ''), $4, false, a.family_id
		  FROM family.attachments a WHERE a.id = $1
		RETURNING id`, photoID, authorID, title, body).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add photo story: %w", err)
	}
	return id, nil
}

// PhotoCounts is how many photographs each subject appears in, so a page can offer
// a gallery only where there is one and say how much is in it.
func (s *Store) PhotoCounts(ctx context.Context, subjectIDs []int64) (map[int64]int, error) {
	rows, err := s.q(ctx).Query(ctx, `
		SELECT ps.subject_id, count(*)
		  FROM family.photo_subjects ps
		  JOIN family.attachments a ON a.id = ps.attachment_id
		 WHERE ps.subject_id = ANY($1) AND a.kind = 'photo'
		 GROUP BY ps.subject_id`, subjectIDs)
	if err != nil {
		return nil, fmt.Errorf("photo counts: %w", err)
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// FamilyOfSubject is which line somebody belongs to, so an upload can be filed
// against the right one without trusting the form to say.
func (s *Store) FamilyOfSubject(ctx context.Context, subjectID int64) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx,
		`SELECT family_id FROM family.subjects WHERE id = $1`, subjectID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("family of subject: %w", err)
	}
	return id, nil
}

// CreatePhotoSubject makes a subject for somebody who is in a photograph and not in
// the tree.
//
// A team photograph has fifteen boys in it and two of them are family; the other
// thirteen are the friends, teammates and neighbours who were there, and being able
// to write down that the boy on the end was Russ is most of what makes the picture
// worth keeping. They are not ancestors and get no questions -- they are people in
// a photograph, which is a thing worth being.
func (s *Store) CreatePhotoSubject(ctx context.Context, familyID int64, name string) (int64, error) {
	// The importer's own slugifier, so a name typed in here and the same name
	// arriving from a GEDCOM land on one subject instead of two.
	slug := subjects.Slugify(name)
	if slug == "" {
		return 0, fmt.Errorf("create photo subject: %q has no name in it", name)
	}
	var id int64
	err := s.q(ctx).QueryRow(ctx, `
		INSERT INTO family.subjects
		  (slug, kind, display_name, sort_order, generation, relation, family_id)
		VALUES ($1, 'individual', $2,
		        coalesce((SELECT max(sort_order) + 1 FROM family.subjects WHERE family_id = $3), 0),
		        0, 'other', $3)
		ON CONFLICT (family_id, slug) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id`, slug, name, familyID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create photo subject %q: %w", name, err)
	}
	return id, nil
}

// FamilyOfPhoto is which line a photograph belongs to.
func (s *Store) FamilyOfPhoto(ctx context.Context, photoID int64) (int64, error) {
	var id int64
	err := s.q(ctx).QueryRow(ctx,
		`SELECT family_id FROM family.attachments WHERE id = $1`, photoID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("family of photo: %w", err)
	}
	return id, nil
}
