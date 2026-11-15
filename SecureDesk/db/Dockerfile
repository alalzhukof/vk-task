FROM postgres:17-alpine@sha256:d3244e8ce3f165ee3c93d9c4a4b1d0fe48b3a89e2287b717fdcbbde92d405065
COPY init.sh /docker-entrypoint-initdb.d/01-init.sh
COPY schema.sql /schema.sql
COPY standby.sh /standby.sh
COPY entrypoint.sh /securedesk-entrypoint.sh
ENTRYPOINT ["sh", "/securedesk-entrypoint.sh"]
RUN chmod 644 /docker-entrypoint-initdb.d/01-init.sh /schema.sql /standby.sh
