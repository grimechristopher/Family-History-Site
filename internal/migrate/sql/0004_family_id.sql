-- Every family table carries the family it belongs to. The column is what the
-- row-level security policies in 0005 filter on, and what the composite foreign
-- keys below use to refuse a row that points across families.

-- Everything that exists today belongs to the one family created in 0003. The
-- default exists only for this backfill and is dropped immediately after, so that
-- from here on a row with no family is an error rather than a silent misfiling.
DO $$
DECLARE
    home bigint := (SELECT id FROM core.families WHERE slug = 'home');
    t    text;
BEGIN
    FOREACH t IN ARRAY ARRAY['people', 'subjects', 'subject_members', 'questions',
                             'question_deferrals', 'entries', 'replies', 'attachments']
    LOOP
        EXECUTE format(
            'ALTER TABLE family.%I ADD COLUMN family_id bigint NOT NULL DEFAULT %s', t, home);
        EXECUTE format('ALTER TABLE family.%I ALTER COLUMN family_id DROP DEFAULT', t);
        EXECUTE format(
            'ALTER TABLE family.%I ADD FOREIGN KEY (family_id) REFERENCES core.families(id) ON DELETE CASCADE', t);
    END LOOP;
END $$;

-- Uniqueness that was global becomes per-family. Two Ancestry exports both contain
-- @I1@; two families may both have a subject called her-father; and the same
-- question text about the same relation is a different question in each family.
ALTER TABLE family.people    DROP CONSTRAINT people_gedcom_id_key;
ALTER TABLE family.subjects  DROP CONSTRAINT subjects_slug_key;
ALTER TABLE family.questions DROP CONSTRAINT questions_import_key_key;

ALTER TABLE family.people    ADD UNIQUE (family_id, gedcom_id);
ALTER TABLE family.subjects  ADD UNIQUE (family_id, slug);
ALTER TABLE family.questions ADD UNIQUE (family_id, import_key);

-- Required as the target of the composite foreign keys.
ALTER TABLE family.people    ADD UNIQUE (family_id, id);
ALTER TABLE family.subjects  ADD UNIQUE (family_id, id);
ALTER TABLE family.questions ADD UNIQUE (family_id, id);
ALTER TABLE family.entries   ADD UNIQUE (family_id, id);

-- A child may not point at a parent in another family. The references to
-- family_members are the valuable ones: an answer cannot be recorded by somebody
-- who is not a member of that family, and the row is refused rather than the check
-- being left to Go.
ALTER TABLE family.questions
    ADD FOREIGN KEY (family_id, subject_id)       REFERENCES family.subjects (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, asked_of_user_id) REFERENCES core.family_members (family_id, user_id);

ALTER TABLE family.subject_members
    ADD FOREIGN KEY (family_id, subject_id) REFERENCES family.subjects (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, person_id)  REFERENCES family.people   (family_id, id) ON DELETE CASCADE;

-- question_id and subject_id are both nullable here: a story has a subject and no
-- question, an answer has a question. Each key holds for whichever is present.
ALTER TABLE family.entries
    ADD FOREIGN KEY (family_id, question_id)    REFERENCES family.questions (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, subject_id)     REFERENCES family.subjects  (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, author_user_id) REFERENCES core.family_members (family_id, user_id);

ALTER TABLE family.replies
    ADD FOREIGN KEY (family_id, entry_id)       REFERENCES family.entries (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, author_user_id) REFERENCES core.family_members (family_id, user_id);

ALTER TABLE family.attachments
    ADD FOREIGN KEY (family_id, entry_id)            REFERENCES family.entries (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, uploaded_by_user_id) REFERENCES core.family_members (family_id, user_id);

ALTER TABLE family.question_deferrals
    ADD FOREIGN KEY (family_id, question_id) REFERENCES family.questions (family_id, id) ON DELETE CASCADE,
    ADD FOREIGN KEY (family_id, user_id)     REFERENCES core.family_members (family_id, user_id);

-- The one reference running the other way, from core into family: a membership
-- points at the person it is, and at whichever subject that member asked to focus
-- their cards on. Scoping both to the family means a focus cannot survive a switch
-- into another one.
ALTER TABLE core.family_members
    ADD FOREIGN KEY (family_id, person_id)              REFERENCES family.people   (family_id, id) ON DELETE SET NULL,
    ADD FOREIGN KEY (family_id, queue_focus_subject_id) REFERENCES family.subjects (family_id, id) ON DELETE SET NULL;

CREATE INDEX questions_by_family ON family.questions (family_id);
CREATE INDEX entries_by_family   ON family.entries   (family_id);
CREATE INDEX subjects_by_family  ON family.subjects  (family_id);
CREATE INDEX people_by_family    ON family.people    (family_id);
