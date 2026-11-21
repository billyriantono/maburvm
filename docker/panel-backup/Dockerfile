# Panel backup/restore image: pg_dump + age + rclone around one script.
#
# Built FROM the same PostgreSQL image as the `postgres` service so pg_dump and
# pg_restore always match the server's major version (a newer server refuses an
# older pg_dump). docker-compose.production.yml passes the tag in.
ARG POSTGRES_IMAGE=postgres:16.14
FROM ${POSTGRES_IMAGE}

RUN apt-get update \
    && apt-get install -y --no-install-recommends age rclone ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

COPY panel-backup.sh /usr/local/bin/panel-backup
RUN chmod 0755 /usr/local/bin/panel-backup

ENTRYPOINT ["/usr/local/bin/panel-backup"]
CMD ["schedule"]
