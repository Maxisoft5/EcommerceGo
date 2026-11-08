CREATE TABLE product_requests (
 seller_id text NOT NULL, request_key text NOT NULL, payload_hash text NOT NULL,
 sku text NOT NULL REFERENCES products(sku), created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(seller_id,request_key)
);
ALTER TABLE products ADD COLUMN fixture_initialized boolean NOT NULL DEFAULT false;
UPDATE products SET fixture_initialized=true WHERE description<>'';
