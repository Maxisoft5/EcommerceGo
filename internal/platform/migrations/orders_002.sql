ALTER TABLE orders ADD COLUMN currency text NOT NULL DEFAULT 'USD';
ALTER TABLE orders ALTER COLUMN currency SET DEFAULT 'RUB';
CREATE INDEX seller_order_items ON orders USING gin(items jsonb_path_ops);
