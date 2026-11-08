CREATE TABLE products (
 sku text PRIMARY KEY, name text NOT NULL, price_cents bigint NOT NULL CHECK(price_cents>0),
 version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE outbox (
 id text PRIMARY KEY, aggregate_id text NOT NULL, payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX pending_outbox ON outbox(created_at) WHERE published_at IS NULL;
