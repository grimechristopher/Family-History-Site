-- One question, several people answering it.
--
-- Robert, Frank, Tony and Inez are asked the same ten questions about their
-- parents, and until now that was ten questions written out four times. The
-- previous migration taught the copies to recognise each other, which fixed the
-- reading of it. This makes them actually one question: four people are asked it,
-- each of them gets it in their own card stack, each writes an answer in their own
-- words, and the four answers sit together underneath it where they can be read as
-- one conversation instead of four.
CREATE TABLE family.question_askees (
    family_id   bigint NOT NULL,
    question_id bigint NOT NULL,
    user_id     bigint NOT NULL REFERENCES core.users (id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, user_id),
    -- Composite, so an askee cannot be attached to a question in another line.
    FOREIGN KEY (family_id, question_id)
        REFERENCES family.questions (family_id, id) ON DELETE CASCADE
);

CREATE INDEX question_askees_user_idx ON family.question_askees (user_id, family_id);

ALTER TABLE family.question_askees ENABLE ROW LEVEL SECURITY;
ALTER TABLE family.question_askees FORCE ROW LEVEL SECURITY;
CREATE POLICY family_isolation ON family.question_askees
    USING      (family_id = ANY (core.current_family_ids()))
    WITH CHECK (family_id = ANY (core.current_family_ids()));
GRANT SELECT, INSERT, UPDATE, DELETE ON family.question_askees TO fhs_app;

-- Every question starts with the one person it was already asked of.
INSERT INTO family.question_askees (family_id, question_id, user_id)
SELECT family_id, id, asked_of_user_id FROM family.questions;

-- Then the copies are folded into one. The lowest id of each set survives and
-- collects the others' askees; anything written against a copy moves across.
--
-- Nothing has been answered anywhere yet, so the moves below are empty in
-- practice. They are written anyway, and left to fail on the unique constraint
-- rather than silently dropping a row, because a migration that quietly loses
-- somebody's answer is worse than one that stops.
CREATE TEMP TABLE merge_map AS
SELECT q.id AS from_id,
       min(q.id) OVER (PARTITION BY q.family_id, q.shared_key) AS to_id,
       q.family_id
  FROM family.questions q
 WHERE q.archived_at IS NULL;
DELETE FROM merge_map WHERE from_id = to_id;

INSERT INTO family.question_askees (family_id, question_id, user_id)
SELECT m.family_id, m.to_id, a.user_id
  FROM merge_map m JOIN family.question_askees a ON a.question_id = m.from_id
ON CONFLICT DO NOTHING;

UPDATE family.entries e SET question_id = m.to_id
  FROM merge_map m WHERE e.question_id = m.from_id;
UPDATE family.question_deferrals d SET question_id = m.to_id
  FROM merge_map m WHERE d.question_id = m.from_id;

DELETE FROM family.questions WHERE id IN (SELECT from_id FROM merge_map);
DROP TABLE merge_map;
