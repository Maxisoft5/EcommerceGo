CREATE TABLE orders (
 id text PRIMARY KEY, user_id text NOT NULL, status text NOT NULL CHECK(status IN ('pending','confirmed','rejected')),
 items jsonb NOT NULL, total_cents bigint NOT NULL CHECK(total_cents>0), reason text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_orders ON orders(user_id,created_at DESC,id);
CREATE TABLE idempotency (
 user_id text NOT NULL, key text NOT NULL, request_hash text NOT NULL, order_id text NOT NULL REFERENCES orders(id),
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(user_id,key)
);
CREATE TABLE inbox (event_id text PRIMARY KEY, processed_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE outbox (
 id text PRIMARY KEY, aggregate_id text NOT NULL, payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX pending_outbox ON outbox(created_at) WHERE published_at IS NULL;
