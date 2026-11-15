#!/bin/sh
set -eu
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  mkdir -p "$PGDATA"
  chmod 700 "$PGDATA"
  chown postgres:postgres "$PGDATA"
  until PGPASSWORD="$REPL_PASSWORD" gosu postgres pg_basebackup \
    -d "host=pg-primary user=replicator application_name=standby" \
    -D "$PGDATA" -Fp -Xs -P -R; do
      sleep 2
  done
fi
exec /usr/local/bin/docker-entrypoint.sh postgres -c wal_log_hints=on
