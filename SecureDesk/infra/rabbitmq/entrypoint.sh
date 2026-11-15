#!/bin/sh
set -eu
export RABBITMQ_DEFAULT_PASS="$(cat /run/securedesk/MQ_PASSWORD)"
export RABBITMQ_ERLANG_COOKIE="$(cat /run/securedesk/MQ_COOKIE)"
# Docker exec / healthchecks do not inherit variables exported by PID 1.
# Keep the CLI's cookie file consistent with the server's persisted secret.
umask 077
printf '%s' "$RABBITMQ_ERLANG_COOKIE" > /var/lib/rabbitmq/.erlang.cookie
chown rabbitmq:rabbitmq /var/lib/rabbitmq/.erlang.cookie
chmod 400 /var/lib/rabbitmq/.erlang.cookie
exec /usr/local/bin/docker-entrypoint.sh rabbitmq-server
