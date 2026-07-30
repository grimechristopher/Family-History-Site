-- Removing somebody has to leave what they wrote.
--
-- A family history that deletes what a person told it is not a family history,
-- and the schema agrees: every answer, reply and photograph carries a foreign key
-- to the membership of the person who wrote it, which is what guarantees an
-- author is really in the family the row belongs to. Deleting the membership is
-- therefore impossible for anybody who has ever written a word, and rightly so.
--
-- So removal is not a delete. The membership stays, as the record that they were
-- here and wrote these things, and is marked as ended. Everything that asks "which
-- families is this person in" skips it, which is every path into the site: the
-- row-level security setting is built from that same list, so a removed member has
-- no way in at all.
ALTER TABLE core.family_members ADD COLUMN removed_at timestamptz;

-- Partial, because almost every row is a current membership and those are the
-- ones every request looks up.
CREATE INDEX family_members_current_idx
    ON core.family_members (user_id, family_id) WHERE removed_at IS NULL;
