-- Photographs become things in their own right.
--
-- Until now a photograph was a child of one answer: you wrote something, and the
-- picture hung off what you wrote. That makes two things impossible. A photograph
-- of Frank and Robert cannot appear on both their pages, because the answer it
-- hangs from belongs to one person. And a photograph on its own -- no story yet,
-- just a picture somebody found in a box -- cannot exist at all.
--
-- The reason this matters is not tidiness. Younger generations do not know what
-- these people looked like. A 1974 gymnastics team photograph has fifteen boys in
-- it and two of them are Ashley's father and uncle; without something recording
-- which two, the picture stops being evidence of anything within a generation.
ALTER TABLE family.attachments ALTER COLUMN entry_id DROP NOT NULL;

-- Composite uniques, so everything pointing at a photograph has to agree with it
-- about which family it belongs to. Same pattern as the rest of the schema: a
-- cross-family row is not something to detect, it is something to make impossible.
ALTER TABLE family.attachments ADD CONSTRAINT attachments_family_id_id_key
    UNIQUE (family_id, id);

-- Who is in the picture, and whereabouts they are in it.
--
-- The point is a percentage of the width and height rather than a pixel, so it
-- stays on the right face whatever size the picture is shown at -- a thumbnail in a
-- gallery, full width on a phone, or a print. Nullable, because knowing somebody is
-- in a photograph is worth recording even when nobody has said where.
CREATE TABLE family.photo_subjects (
    family_id     bigint NOT NULL,
    attachment_id bigint NOT NULL,
    subject_id    bigint NOT NULL,
    point_x       numeric(5,2) CHECK (point_x >= 0 AND point_x <= 100),
    point_y       numeric(5,2) CHECK (point_y >= 0 AND point_y <= 100),
    added_by_user_id bigint NOT NULL REFERENCES core.users (id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (attachment_id, subject_id),
    FOREIGN KEY (family_id, attachment_id)
        REFERENCES family.attachments (family_id, id) ON DELETE CASCADE,
    FOREIGN KEY (family_id, subject_id)
        REFERENCES family.subjects (family_id, id) ON DELETE CASCADE
);

CREATE INDEX photo_subjects_subject_idx ON family.photo_subjects (subject_id);

-- An entry can be about a photograph, which is what makes writing a story about one
-- and replying to somebody else's the same machinery as everywhere else: replies
-- already hang off entries, so they need no changes at all.
ALTER TABLE family.entries ADD COLUMN attachment_id bigint;
ALTER TABLE family.entries ADD CONSTRAINT entries_family_id_attachment_id_fkey
    FOREIGN KEY (family_id, attachment_id)
    REFERENCES family.attachments (family_id, id) ON DELETE CASCADE;
CREATE INDEX entries_attachment_idx ON family.entries (attachment_id);

ALTER TABLE family.photo_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE family.photo_subjects FORCE ROW LEVEL SECURITY;
CREATE POLICY family_isolation ON family.photo_subjects
    USING      (family_id = ANY (core.current_family_ids()))
    WITH CHECK (family_id = ANY (core.current_family_ids()));
GRANT SELECT, INSERT, UPDATE, DELETE ON family.photo_subjects TO fhs_app;
