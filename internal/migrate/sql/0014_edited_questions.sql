-- Editing and removing a question, in a way a re-import respects.
--
-- Both of those need recording rather than just doing, because the importer runs the
-- same prompts file over the same rows every time: it sets body from the file and
-- clears archived_at, so a reworded question would snap back to the file's wording
-- and a removed one would reappear. A hand edit is more specific than the file it
-- came from and has to win.
ALTER TABLE family.questions
    ADD COLUMN edited_at         timestamptz,
    ADD COLUMN edited_by_user_id bigint REFERENCES core.users (id),
    ADD COLUMN deleted_at        timestamptz,
    ADD COLUMN deleted_by_user_id bigint REFERENCES core.users (id);

-- Removing a question sets archived_at as well, which is what every page already
-- filters on -- so it disappears everywhere without a single query needing to change.
-- deleted_at is what makes it stay gone.
COMMENT ON COLUMN family.questions.deleted_at IS
    'Removed by hand. archived_at is set with it so existing filters hide it; this is
     what stops a re-import bringing it back.';
