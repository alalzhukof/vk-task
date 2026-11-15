CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username varchar(32) UNIQUE NOT NULL,
    password_hash text NOT NULL,
    revision bigint NOT NULL DEFAULT 0
);
CREATE TABLE sessions (
    token_hash char(64) PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '1 hour'
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id uuid NOT NULL REFERENCES users(id),
    title varchar(160) NOT NULL CHECK(length(trim(title))>0),
    description varchar(2000) NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','in_progress','done')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tickets_owner ON tickets(owner_id,created_at DESC);
CREATE TABLE outbox (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users(id),
    ticket_id uuid NOT NULL,
    action text NOT NULL CHECK(action IN ('created','updated','deleted')),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_pending ON outbox(created_at) WHERE published_at IS NULL;
CREATE TABLE audit (
    event_id uuid PRIMARY KEY,
    owner_id uuid NOT NULL,
    ticket_id uuid NOT NULL,
    action text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_owner ON audit(owner_id,processed_at DESC);
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE desk TO desk_api, desk_worker;
GRANT USAGE ON SCHEMA public TO desk_api, desk_worker;
GRANT SELECT,INSERT,UPDATE,DELETE ON users,sessions,tickets TO desk_api;
GRANT INSERT ON outbox TO desk_api;
GRANT SELECT ON audit TO desk_api;
GRANT SELECT,UPDATE ON outbox TO desk_worker;
GRANT INSERT,SELECT ON audit TO desk_worker;
