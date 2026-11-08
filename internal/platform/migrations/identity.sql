CREATE TABLE users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE, password_hash text NOT NULL,
 role text NOT NULL CHECK(role IN ('customer','admin')), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE refresh_tokens (
 hash text PRIMARY KEY, family text NOT NULL, user_id text NOT NULL REFERENCES users(id),
 expires_at timestamptz NOT NULL, used_at timestamptz, revoked boolean NOT NULL DEFAULT false
);
CREATE INDEX refresh_family ON refresh_tokens(family);
CREATE INDEX refresh_expiry ON refresh_tokens(expires_at);
