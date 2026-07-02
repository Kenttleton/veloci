#!/usr/bin/env bash
# scripts/init-db.sh
# Runs inside the postgres container as the superuser.
# Creates both databases and their dedicated users.
set -euo pipefail

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<-EOSQL
  CREATE USER ${VELOCI_AUTH_DB_USER} WITH PASSWORD '${VELOCI_AUTH_DB_PASSWORD}';
  CREATE DATABASE ${VELOCI_AUTH_DB} OWNER ${VELOCI_AUTH_DB_USER};

  CREATE USER ${VELOCI_APP_DB_USER} WITH PASSWORD '${VELOCI_APP_DB_PASSWORD}';
  CREATE DATABASE ${VELOCI_APP_DB} OWNER ${VELOCI_APP_DB_USER};
EOSQL

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_AUTH_DB_USER}" \
  --dbname "${VELOCI_AUTH_DB}" \
  -f /migrations/auth/001_auth_schema.sql

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_APP_DB_USER}" \
  --dbname "${VELOCI_APP_DB}" \
  -f /migrations/app/001_app_schema.sql

psql -v ON_ERROR_STOP=1 \
  --username "${VELOCI_APP_DB_USER}" \
  --dbname "${VELOCI_APP_DB}" \
  -f /migrations/app/002_rbac_seed.sql
