-- Family isolation is enforced by Postgres, not by remembering a WHERE clause in
-- every one of a hundred queries. A query that forgets to scope itself returns
-- nothing; a write that names the wrong family is refused.

-- The role the server connects as. Created here so a fresh database is usable with
-- no manual step; on a real deployment the password is changed by hand afterwards
-- and put in DATABASE_URL:
--
--   ALTER ROLE fhs_app PASSWORD '<something>';
--   REASSIGN OWNED BY postgres TO fhs_app;
--
-- DATABASE_URL must not name a superuser. Postgres exempts superusers from every
-- policy, so the whole of this file would be created, look correct, and do nothing.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'fhs_app') THEN
        CREATE ROLE fhs_app LOGIN PASSWORD 'testpw';
    END IF;
END $$;

GRANT USAGE ON SCHEMA core, family TO fhs_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA core, family TO fhs_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA core, family TO fhs_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA core, family
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO fhs_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA core, family
    GRANT USAGE, SELECT ON SEQUENCES TO fhs_app;

-- ENABLE turns policies on for everyone except the table owner. FORCE removes that
-- exemption, which is what lets one ordinary role own the tables, run the
-- migrations and serve the site while still being subject to its own policies --
-- and so keeps a single DATABASE_URL and one migration path.
--
-- current_setting(..., true) yields NULL when the setting is absent, so the
-- comparison is NULL and the policy matches nothing. The default is no data.
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['people', 'subjects', 'subject_members', 'questions',
                             'question_deferrals', 'entries', 'replies', 'attachments']
    LOOP
        EXECUTE format('ALTER TABLE family.%I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE family.%I FORCE  ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY family_isolation ON family.%I
                USING      (family_id = current_setting('app.family_id', true)::bigint)
                WITH CHECK (family_id = current_setting('app.family_id', true)::bigint)
        $f$, t);
    END LOOP;
END $$;
