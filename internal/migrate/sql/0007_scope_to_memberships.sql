-- A function rather than the expression inline, so all eight policies say the same
-- thing and there is one place to read what "allowed" means.
--
-- An unset or empty setting yields an empty array, so nothing matches: the default
-- is no data, exactly as before. STABLE lets the planner call it once per query
-- rather than once per row.
-- In core, not public: the application role owns core and has no rights on public,
-- so an unqualified name here fails on a correctly-privileged deployment.
CREATE OR REPLACE FUNCTION core.current_family_ids() RETURNS bigint[]
LANGUAGE sql STABLE AS $fn$
    SELECT coalesce(
        string_to_array(nullif(current_setting('app.family_ids', true), ''), ',')::bigint[],
        ARRAY[]::bigint[])
$fn$;

-- Scope every read to the families somebody belongs to, rather than to one family
-- they picked.
--
-- The first version answered "which family is this page about", so a request saw
-- exactly one. But being in four is normal here -- two parents' lines each for a
-- married couple -- and making people switch between four half-empty pages is
-- worse than one list they can filter. So the policy now answers the question that
-- actually matters for safety: which families is this person entitled to at all.
--
-- Nothing is weakened by it. Frank still cannot see Violeta's line, because he is
-- not a member of it; the setting is built from core.family_members and never from
-- anything the browser sends. What changes is that a page may show two lines at
-- once when the person belongs to both, and the filters narrow it from there.
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['people', 'subjects', 'subject_members', 'questions',
                             'question_deferrals', 'entries', 'replies', 'attachments']
    LOOP
        EXECUTE format('DROP POLICY IF EXISTS family_isolation ON family.%I', t);
        EXECUTE format($f$
            CREATE POLICY family_isolation ON family.%I
                USING      (family_id = ANY (core.current_family_ids()))
                WITH CHECK (family_id = ANY (core.current_family_ids()))
        $f$, t);
    END LOOP;
END $$;
