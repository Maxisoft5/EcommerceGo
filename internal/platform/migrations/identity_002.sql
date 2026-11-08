ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK(role IN ('customer','seller','admin'));
ALTER TABLE users ADD COLUMN shop_name text NOT NULL DEFAULT '';
