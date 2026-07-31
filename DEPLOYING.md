# Putting this on a real server

Nothing here has ever run outside a laptop. This is the order to do it in, and the
two things that will silently ruin it if you get them wrong.

## The two that matter

**1. The application must not connect as a superuser or as the table owner.**

Every family is kept apart by row-level security. A superuser is exempt from every
policy, and a table owner is exempt unless `FORCE ROW LEVEL SECURITY` is set — it
is set here, but do not rely on that alone. If `DATABASE_URL` names the wrong role,
every page still works and every family sees every other family's questions.

This has already happened once in development: the dev server ran as `postgres`,
and Ashley's tree rendered Chris's grandparents.

**2. `DEV_LOGIN` must not be set.**

It registers `/dev/login/{family}/{name}`, which signs in as anybody with no link
and no password. Leave the variable out entirely.

## Order

1. **Database.** Postgres 16. Create the application role:

   ```sql
   CREATE ROLE fhs_app LOGIN PASSWORD '...';
   ```

   Migrations run at start-up as whoever `DATABASE_URL` names, and they create and
   alter tables — so the first start-up needs an owner, and every start-up after
   that should be `fhs_app`. Either run the server once with an owning role and
   then switch, or grant `fhs_app` ownership of the `family` and `core` schemas and
   leave it. `scripts/testdb.sh` does the latter, which is what development uses.

2. **Environment.** Copy `.env.example`. Everything without a default is required
   and the server refuses to start without it. `SUPABASE_SERVICE_ROLE_KEY` is a
   server-side key: it bypasses every Supabase policy and must never reach a
   browser.

3. **Supabase redirect allowlist.** Add `BASE_URL` + `/auth/callback` to
   `GOTRUE_URI_ALLOW_LIST`. GoTrue does not reject a redirect that is not on the
   list — it silently substitutes `SITE_URL`, which looks exactly like a broken
   login and is not logged anywhere.

4. **Import.** Copy `lines.example.conf` to `lines.conf`, put the tree files in
   `tree/` and the prompts in `prompts/`, then:

   ```sh
   DATABASE_URL=<owner> ADMIN_EMAIL=you@example.com ./scripts/import.sh
   ```

   The importer creates, archives and deletes rows across the schema, so it runs as
   an owning role rather than as `fhs_app`.

   The tree is JSON, not a GEDCOM. Nothing needs a GEDCOM to deploy: it is a 1980s
   line format that arrives from whichever site the family happens to use, carries a
   hundred thousand lines of sources that nothing here reads, and writes surnames in
   capitals. Convert an export once and keep the JSON, which is readable and can be
   corrected by hand:

   ```sh
   go run ./cmd/treejson -gedcom "export.ged" -out tree/one.json \
     -root "James Andrew /Grime/" -root "Lori Ann /Ayres/" \
     -extra-names "Lela /Wisehaupt/"
   ```

   Name the people each line is drawn from and it keeps only what the import reads:
   their ancestors, their brothers and sisters, those siblings' children, and the
   spouses of all of them. That is seventy-two people out of two thousand for the
   Grime and Ayres lines. Without `-root` it writes the whole export, which is
   thousands of records reached through fifth cousins and their in-laws -- a file
   nobody can read is a file nobody will correct, which was the reason for moving off
   the GEDCOM in the first place.

   It normalises the shouting on the way through and folds any duplicate records,
   saying which. A `.ged` path still works in `lines.conf` if you would rather import
   an export directly.

5. **Sign-in addresses.** The import seeds whatever `lines.conf` says. To move
   somebody to their real address without orphaning their questions:

   ```sh
   make set-email NAME="Frank Lucero" EMAIL=frank@hisdomain.com
   ```

   That updates this site's allowlist and creates the Supabase account. Both have
   to agree or the magic link is never sent.

6. **Check it before telling anyone.** Sign in as one person and confirm they see
   their own line and nothing else. The isolation tests prove the policies hold,
   but they cannot prove `DATABASE_URL` points where you think it does.

   ```sh
   psql "$DATABASE_URL" -c "select current_user, usesuper from pg_user where usename = current_user"
   ```

   `usesuper` must be `f`.
