#!/bin/sh
set -eu
export POSTGRES_PASSWORD="$(cat /run/securedesk/PG_PASSWORD)"
export API_DB_PASSWORD="$(cat /run/securedesk/API_DB_PASSWORD)"
export WORKER_DB_PASSWORD="$(cat /run/securedesk/WORKER_DB_PASSWORD)"
export REPL_PASSWORD="$(cat /run/securedesk/REPL_PASSWORD)"
if [ "${1:-}" = standby ]; then
  exec sh /standby.sh
fi
exec /usr/local/bin/docker-entrypoint.sh "$@"
