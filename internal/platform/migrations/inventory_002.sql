ALTER TABLE stock ADD COLUMN seller_id text NOT NULL DEFAULT 'mesto-studio';
ALTER TABLE stock ADD COLUMN version bigint NOT NULL DEFAULT 1;
CREATE TABLE stock_adjustments (
 seller_id text NOT NULL, request_key text NOT NULL, sku text NOT NULL REFERENCES stock(sku), delta integer NOT NULL,
 available_after integer NOT NULL, version_after bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(seller_id,request_key)
);
CREATE TABLE outbox (
 id text PRIMARY KEY, aggregate_id text NOT NULL, payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), published_at timestamptz
);
CREATE INDEX pending_outbox ON outbox(created_at) WHERE published_at IS NULL;
