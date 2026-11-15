#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname desk \
  --set=api_password="$API_DB_PASSWORD" --set=worker_password="$WORKER_DB_PASSWORD" --set=repl_password="$REPL_PASSWORD" <<'SQL'
CREATE ROLE desk_api LOGIN PASSWORD :'api_password';
CREATE ROLE desk_worker LOGIN PASSWORD :'worker_password';
CREATE ROLE replicator REPLICATION LOGIN PASSWORD :'repl_password';
ALTER SYSTEM SET wal_log_hints = on;
ALTER SYSTEM SET wal_keep_size = '256MB';
SQL
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname desk -f /schema.sql
printf '\nhost replication replicator 172.29.20.0/24 scram-sha-256\n' >> "$PGDATA/pg_hba.conf"
