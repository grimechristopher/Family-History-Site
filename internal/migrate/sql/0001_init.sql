CREATE SCHEMA IF NOT EXISTS family;

CREATE TABLE family.people (
    id         bigserial PRIMARY KEY,
    gedcom_id  text UNIQUE NOT NULL,
    given_name text NOT NULL DEFAULT '',
    surname    text NOT NULL DEFAULT '',
    sex        char(1),
    birth_year int,
    death_year int,
    father_id  bigint REFERENCES family.people(id),
    mother_id  bigint REFERENCES family.people(id)
);

CREATE TABLE family.subjects (
    id           bigserial PRIMARY KEY,
    slug         text UNIQUE NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('individual', 'couple', 'group')),
    display_name text NOT NULL,
    sort_order   int NOT NULL DEFAULT 0
);

CREATE TABLE family.subject_members (
    subject_id bigint NOT NULL REFERENCES family.subjects(id) ON DELETE CASCADE,
    person_id  bigint NOT NULL REFERENCES family.people(id),
    PRIMARY KEY (subject_id, person_id)
);

-- Keyed on email rather than the Supabase id, because rows are seeded by hand
-- before anyone has ever logged in. This table is also the allowlist: a
-- verified Supabase login with no row here gets no access, which is what keeps
-- portfolio signups out of the family history.
CREATE TABLE family.users (
    id                     bigserial PRIMARY KEY,
    email                  text UNIQUE NOT NULL,
    supabase_user_id       uuid UNIQUE,
    display_name           text NOT NULL,
    person_id              bigint REFERENCES family.people(id),
    role                   text NOT NULL CHECK (role IN ('admin', 'contributor')),
    queue_mode             text NOT NULL DEFAULT 'in_order'
                             CHECK (queue_mode IN ('in_order', 'shuffle', 'one_subject')),
    queue_seed             bigint NOT NULL DEFAULT 0,
    queue_focus_subject_id bigint REFERENCES family.subjects(id),
    digest_enabled         boolean NOT NULL DEFAULT true,
    last_seen_at           timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE family.questions (
    id                 bigserial PRIMARY KEY,
    subject_id         bigint NOT NULL REFERENCES family.subjects(id),
    asked_of_user_id   bigint NOT NULL REFERENCES family.users(id),
    topic              text,
    body               text NOT NULL,
    sort_order         int NOT NULL DEFAULT 0,
    is_proposed        boolean NOT NULL DEFAULT false,
    source             text NOT NULL CHECK (source IN ('import', 'user')),
    created_by_user_id bigint REFERENCES family.users(id),
    import_key         text UNIQUE,
    created_at         timestamptz NOT NULL DEFAULT now(),
    archived_at        timestamptz
);

CREATE INDEX questions_queue_idx
    ON family.questions (asked_of_user_id, sort_order)
    WHERE archived_at IS NULL;

-- Swiping a card away records a deferral. There is no "declined" state: every
-- question returns to the queue forever, by explicit decision.
CREATE TABLE family.question_deferrals (
    question_id bigint NOT NULL REFERENCES family.questions(id) ON DELETE CASCADE,
    user_id     bigint NOT NULL REFERENCES family.users(id) ON DELETE CASCADE,
    deferred_at timestamptz NOT NULL DEFAULT now(),
    defer_count int NOT NULL DEFAULT 1,
    PRIMARY KEY (question_id, user_id)
);

-- Answers and stories share one table. question_id IS NULL means "story".
-- Postgres treats NULLs as distinct in unique constraints, so the constraint
-- below allows exactly one answer per person per question while permitting
-- unlimited stories per person.
--
-- is_draft has no default deliberately: autosave writes true, the explicit save
-- writes false, and forcing both call sites to state it removes the chance of an
-- answer silently never counting as answered.
CREATE TABLE family.entries (
    id             bigserial PRIMARY KEY,
    question_id    bigint REFERENCES family.questions(id) ON DELETE CASCADE,
    subject_id     bigint REFERENCES family.subjects(id),
    author_user_id bigint NOT NULL REFERENCES family.users(id),
    title          text,
    body           text NOT NULL,
    is_draft       boolean NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (question_id, author_user_id)
);

CREATE INDEX entries_author_idx ON family.entries (author_user_id);

CREATE TABLE family.replies (
    id              bigserial PRIMARY KEY,
    entry_id        bigint NOT NULL REFERENCES family.entries(id) ON DELETE CASCADE,
    parent_reply_id bigint REFERENCES family.replies(id) ON DELETE CASCADE,
    author_user_id  bigint NOT NULL REFERENCES family.users(id),
    body            text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX replies_entry_idx ON family.replies (entry_id);

-- duration_seconds and transcript are unused until audio lands in phase 6.
-- Four idle columns are cheaper than a migration later.
CREATE TABLE family.attachments (
    id                  bigserial PRIMARY KEY,
    entry_id            bigint NOT NULL REFERENCES family.entries(id) ON DELETE CASCADE,
    kind                text NOT NULL CHECK (kind IN ('photo', 'audio')),
    storage_path        text NOT NULL,
    caption             text,
    mime_type           text NOT NULL,
    size_bytes          bigint NOT NULL,
    width               int,
    height              int,
    duration_seconds    int,
    transcript          text,
    sort_order          int NOT NULL DEFAULT 0,
    uploaded_by_user_id bigint NOT NULL REFERENCES family.users(id),
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_entry_idx ON family.attachments (entry_id);

-- Only a SHA-256 digest of the cookie value is stored, so a leaked database
-- dump cannot be replayed as a live session.
CREATE TABLE family.sessions (
    id           bigserial PRIMARY KEY,
    token_hash   bytea UNIQUE NOT NULL,
    user_id      bigint NOT NULL REFERENCES family.users(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL DEFAULT now(),
    user_agent   text
);

CREATE INDEX sessions_expires_idx ON family.sessions (expires_at);
