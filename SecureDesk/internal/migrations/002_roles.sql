ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT 'user' CHECK (role IN ('user','operator','admin'));
ALTER TABLE tickets ADD COLUMN assigned_operator_id uuid REFERENCES users(id);
ALTER TABLE tickets ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX tickets_operator_created ON tickets(assigned_operator_id, created_at DESC);
CREATE TABLE comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 ticket_id uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
 author_id uuid NOT NULL REFERENCES users(id),
 body varchar(2000) NOT NULL CHECK (length(btrim(body)) > 0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_ticket_created ON comments(ticket_id, created_at, id);
CREATE TABLE role_changes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 actor_id uuid NOT NULL REFERENCES users(id),
 user_id uuid NOT NULL REFERENCES users(id),
 old_role text NOT NULL,
 new_role text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox DROP CONSTRAINT outbox_action_check;
ALTER TABLE outbox ADD CHECK (action IN ('created','updated','deleted','assigned','commented'));
GRANT SELECT, INSERT ON comments, role_changes TO desk_api;
