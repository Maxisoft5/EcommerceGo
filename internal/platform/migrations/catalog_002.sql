ALTER TABLE products ADD COLUMN seller_id text NOT NULL DEFAULT 'mesto-studio';
ALTER TABLE products ADD COLUMN seller_name text NOT NULL DEFAULT 'Место · Студия';
ALTER TABLE products ADD COLUMN description text NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN category text NOT NULL DEFAULT 'electronics';
ALTER TABLE products ADD COLUMN image_key text NOT NULL DEFAULT 'headphones';
ALTER TABLE products ADD COLUMN old_price_cents bigint NOT NULL DEFAULT 0 CHECK(old_price_cents>=0);
ALTER TABLE products ADD COLUMN initial_stock integer NOT NULL DEFAULT 0 CHECK(initial_stock BETWEEN 0 AND 1000000);
ALTER TABLE products ADD COLUMN currency text NOT NULL DEFAULT 'RUB' CHECK(currency='RUB');
CREATE INDEX seller_products ON products(seller_id,updated_at DESC);
