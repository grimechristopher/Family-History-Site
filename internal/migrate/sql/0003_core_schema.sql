-- Identity is shared across families; family data is not. Splitting them is what
-- lets one person be an admin in one family and a contributor in another, signing
-- in once.
CREATE SCHEMA IF NOT EXISTS core;

CREATE TABLE core.families (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug         text NOT NULL UNIQUE,
    display_name text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Moved rather than copied, so every foreign key already pointing at them still
-- resolves and no id changes.
ALTER TABLE family.users    SET SCHEMA core;
ALTER TABLE family.sessions SET SCHEMA core;

-- Everything that is true of a person only within one family. These were columns
-- on users, which was wrong: a role, a place in the card stack and which person in
-- the tree you are all differ per family.
CREATE TABLE core.family_members (
    family_id              bigint  NOT NULL REFERENCES core.families(id) ON DELETE CASCADE,
    user_id                bigint  NOT NULL REFERENCES core.users(id)    ON DELETE CASCADE,
    role                   text    NOT NULL DEFAULT 'contributor',
    person_id              bigint,
    queue_mode             text    NOT NULL DEFAULT 'all',
    queue_seed             bigint  NOT NULL DEFAULT floor(random() * 1e9)::bigint,
    queue_focus_subject_id bigint,
    digest_enabled         boolean NOT NULL DEFAULT true,
    created_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (family_id, user_id)
);

-- One family owns everything that exists today. The slug is deliberately generic:
-- this migration cannot know whose data it is, and cmd/family can rename it.
INSERT INTO core.families (slug, display_name) VALUES ('home', 'Our family');

INSERT INTO core.family_members
       (family_id, user_id, role, person_id, queue_mode, queue_seed,
        queue_focus_subject_id, digest_enabled)
SELECT (SELECT id FROM core.families WHERE slug = 'home'),
       u.id, u.role, u.person_id, u.queue_mode, u.queue_seed,
       u.queue_focus_subject_id, u.digest_enabled
  FROM core.users u;

ALTER TABLE core.users
    DROP COLUMN role,
    DROP COLUMN person_id,
    DROP COLUMN queue_mode,
    DROP COLUMN queue_seed,
    DROP COLUMN queue_focus_subject_id,
    DROP COLUMN digest_enabled;

-- An invitation is the authorisation to join a family, so its token is hashed
-- exactly as a session token is: a leaked database dump must not be replayable as
-- a way in.
CREATE TABLE core.invites (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    family_id           bigint NOT NULL REFERENCES core.families(id) ON DELETE CASCADE,
    email               text   NOT NULL,
    invited_by_user_id  bigint NOT NULL REFERENCES core.users(id),
    token_hash          bytea  NOT NULL UNIQUE,
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,
    accepted_at         timestamptz,
    accepted_by_user_id bigint REFERENCES core.users(id),
    revoked_at          timestamptz
);

-- One live invitation per address per family, so inviting somebody twice reports
-- that it is already pending rather than sending a second competing link.
CREATE UNIQUE INDEX invites_one_live_per_email
    ON core.invites (family_id, lower(email))
 WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Sign-in looks an address up here when it has no membership yet.
CREATE INDEX invites_live_by_email ON core.invites (lower(email))
 WHERE accepted_at IS NULL AND revoked_at IS NULL;
