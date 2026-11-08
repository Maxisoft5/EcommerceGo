#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v identity_password="$IDENTITY_DB_PASSWORD" -v catalog_password="$CATALOG_DB_PASSWORD" -v inventory_password="$INVENTORY_DB_PASSWORD" <<'SQL'
SELECT format('CREATE ROLE identity LOGIN PASSWORD %L', :'identity_password') \gexec
SELECT format('CREATE ROLE catalog LOGIN PASSWORD %L', :'catalog_password') \gexec
SELECT format('CREATE ROLE inventory LOGIN PASSWORD %L', :'inventory_password') \gexec
CREATE DATABASE identity OWNER identity;
CREATE DATABASE catalog OWNER catalog;
CREATE DATABASE inventory OWNER inventory;
REVOKE CONNECT ON DATABASE identity FROM PUBLIC;
REVOKE CONNECT ON DATABASE catalog FROM PUBLIC;
REVOKE CONNECT ON DATABASE inventory FROM PUBLIC;
GRANT CONNECT ON DATABASE identity TO identity;
GRANT CONNECT ON DATABASE catalog TO catalog;
GRANT CONNECT ON DATABASE inventory TO inventory;
SQL
