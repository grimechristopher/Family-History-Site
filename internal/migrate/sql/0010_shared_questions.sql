-- The same question asked of several people is several rows, and nothing said so.
--
-- Robert, Frank, Tony and Inez are asked the same ten questions about their
-- parents. That is right: each of them has to write his or her own answer, and a
-- card stack that skipped Tony because Frank had already answered would lose three
-- quarters of what this is for. What was wrong is that the four rows were
-- unrelated, so the list showed one question four times, and Frank's answer and
-- Tony's answer to the same question could not be read side by side.
--
-- shared_key is what makes them one question in four hands. Generated rather than
-- assigned, so nothing can forget to set it: two questions in a line, about the
-- same person, whose text is the same once whitespace and case are ignored, are
-- the same question. That covers the importer and equally a question somebody adds
-- by hand for two brothers.
ALTER TABLE family.questions
    ADD COLUMN shared_key text GENERATED ALWAYS AS (
        md5(coalesce(subject_id::text, '-') || '|' ||
            lower(regexp_replace(btrim(body), '\s+', ' ', 'g')))
    ) STORED;

-- Every lookup is "the other rows of this question in this line".
CREATE INDEX questions_shared_key_idx ON family.questions (family_id, shared_key);
