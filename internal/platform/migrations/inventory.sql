CREATE TABLE stock (sku text PRIMARY KEY, available integer NOT NULL CHECK(available>=0));
CREATE TABLE reservations (
 order_id text PRIMARY KEY, request_hash text NOT NULL, accepted boolean NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO stock(sku,available) VALUES ('sku-keyboard',1000),('sku-headphones',1000),('sku-monitor',1000);
