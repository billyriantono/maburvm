#!/usr/bin/env bash
# Panel disaster-recovery backup and restore (docs/DEPLOYMENT.md section 9).
#
#   panel-backup schedule                       back up every day at BACKUP_TIME (the container default)
#   panel-backup once                           back up now
#   panel-backup check                          validate the configuration and database; write a probe file to the destination
#   panel-backup healthcheck                    exit 1 if the last check or backup failed
#   panel-backup restore ARCHIVE IDENTITY [--force]
#   panel-backup host-config ARCHIVE IDENTITY DIR
#
# One archive holds a pg_dump of the panel database, the panel data directory
# (secrets.json) and the host's .env.production + deploy/secrets. It is encrypted
# to age PUBLIC keys, so nothing on the panel host can decrypt it.
set -euo pipefail

PREFIX=maburvm-panel
CHECK_FILE=panel-backup-check.txt
STATUS_FILE="${BACKUP_STATUS_FILE:-/tmp/panel-backup.status}"
PANEL_DATA_DIR="${PANEL_DATA_DIR:-/panel-data}"
HOST_DIR="${HOST_DIR:-/host}"
BACKUP_TIME="${BACKUP_TIME:-02:30}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-30}"
export RCLONE_CONFIG="${RCLONE_CONFIG:-$HOST_DIR/deploy/secrets/rclone.conf}"
export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-15}"
DEST_HELP="BACKUP_DEST must be an rclone remote (name:path, with the remote defined in deploy/secrets/rclone.conf). A plain path would keep the backup on this host."

log() { printf '%s panel-backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }
set_status() { printf '%s %s\n' "$1" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STATUS_FILE"; }

load_db_password() {
  if [ -z "${PGPASSWORD:-}" ] && [ -n "${DB_PASSWORD_FILE:-}" ]; then
    [ -r "$DB_PASSWORD_FILE" ] || die "cannot read DB_PASSWORD_FILE=$DB_PASSWORD_FILE"
    PGPASSWORD="$(cat "$DB_PASSWORD_FILE")"
    export PGPASSWORD
  fi
}

# Prints why backups are off; prints nothing when they are configured.
unconfigured_reason() {
  if [ -z "${BACKUP_DEST:-}" ]; then
    echo "BACKUP_DEST is empty"
  elif [ -z "${BACKUP_AGE_RECIPIENTS:-}" ]; then
    echo "BACKUP_AGE_RECIPIENTS is empty"
  fi
}

# Never echoes an entry: a private key pasted here by mistake must not reach the log.
load_recipients() {
  RECIPIENTS=()
  local r
  for r in $(printf '%s' "$BACKUP_AGE_RECIPIENTS" | tr ',' ' '); do
    case "$r" in
      AGE-SECRET-KEY-*) die "BACKUP_AGE_RECIPIENTS holds a PRIVATE key. Put the public key (age1...) there, keep the private key off this host, and treat that key as exposed." ;;
      age1*) RECIPIENTS+=(-r "$r") ;;
      *) die "BACKUP_AGE_RECIPIENTS has an entry that is not an age public key (age1...)" ;;
    esac
  done
  [ "${#RECIPIENTS[@]}" -gt 0 ] || die "BACKUP_AGE_RECIPIENTS holds no keys"
}

validate() {
  local reason
  reason="$(unconfigured_reason)"
  [ -z "$reason" ] || die "not configured: $reason"
  # A plain path is the container's own disk: on this host, and gone with the container.
  case "$BACKUP_DEST" in
    /* | .*) die "$DEST_HELP" ;;
    ?*:*) ;;
    *) die "$DEST_HELP" ;;
  esac
  load_recipients
  age "${RECIPIENTS[@]}" < /dev/null > /dev/null 2>&1 ||
    die "BACKUP_AGE_RECIPIENTS holds a key age does not accept; copy the public key (age1...) again"
  case "$BACKUP_RETENTION_DAYS" in '' | *[!0-9]*) die "BACKUP_RETENTION_DAYS must be a whole number of days (0 keeps everything)" ;; esac
  case "$BACKUP_TIME" in [01][0-9]:[0-5][0-9] | 2[0-3]:[0-5][0-9]) ;; *) die "BACKUP_TIME must be HH:MM (24 hour)" ;; esac
  [ -s "$PANEL_DATA_DIR/secrets.json" ] || die "$PANEL_DATA_DIR/secrets.json is missing; a backup without it cannot decrypt the secrets stored in the database"
  load_db_password
}

check() {
  validate
  psql -X -Atc 'select 1' >/dev/null || die "cannot reach the database"
  # A real write: listing or mkdir can succeed with credentials that cannot upload.
  printf 'written by panel-backup check at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" |
    rclone rcat "$BACKUP_DEST/$CHECK_FILE" || die "cannot write to the backup destination"
  log "configuration ok: database reachable, destination writable"
}

backup() {
  validate
  umask 077
  local name
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  name="$PREFIX-$(date -u +%Y%m%dT%H%M%SZ).tar.age"

  log "dumping database ${PGDATABASE:-}"
  pg_dump --format=custom --file="$WORK/db.dump"
  pg_restore --list "$WORK/db.dump" >/dev/null

  tar -C "$PANEL_DATA_DIR" -cf "$WORK/panel-data.tar" .

  mkdir "$WORK/host-config"
  if [ -f "$HOST_DIR/.env.production" ]; then cp "$HOST_DIR/.env.production" "$WORK/host-config/"; fi
  if [ -d "$HOST_DIR/deploy/secrets" ]; then
    mkdir "$WORK/host-config/deploy"
    cp -R "$HOST_DIR/deploy/secrets" "$WORK/host-config/deploy/"
  fi

  {
    echo "format=1"
    echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "database=${PGDATABASE:-}"
    echo "server_version=$(psql -X -Atc 'show server_version')"
    echo "pg_dump=$(pg_dump --version)"
    echo "last_migration=$(psql -X -Atc 'select max(version) from schema_migrations' 2>/dev/null || echo unknown)"
  } > "$WORK/MANIFEST"

  tar -C "$WORK" -cf - MANIFEST db.dump panel-data.tar host-config | age "${RECIPIENTS[@]}" -o "$WORK/$name"
  log "uploading $name ($(wc -c < "$WORK/$name" | tr -d ' ') bytes)"
  rclone copyto "$WORK/$name" "$BACKUP_DEST/$name"

  if [ "$BACKUP_RETENTION_DAYS" -gt 0 ]; then
    # Runs only after a successful upload, so the newest backup always survives.
    rclone delete "$BACKUP_DEST" --max-depth 1 --include "$PREFIX-*.tar.age" --min-age "${BACKUP_RETENTION_DAYS}d" -v ||
      log "WARNING: could not remove backups older than $BACKUP_RETENTION_DAYS days"
  fi
  log "backup ok: $name"
}

# errexit is ignored inside `if`, so the work runs as its own process.
run_recorded() {
  if bash "$0" "$1"; then
    set_status ok
    return 0
  fi
  set_status failed
  return 1
}

once() {
  if run_recorded _backup; then
    if [ -n "${BACKUP_HEARTBEAT_URL:-}" ]; then
      curl -fsS -m 20 --retry 3 -o /dev/null "$BACKUP_HEARTBEAT_URL" || log "WARNING: heartbeat ping failed"
    fi
    return 0
  fi
  log "ERROR: backup FAILED; the newest backup at the destination is an older one"
  return 1
}

# Seconds from now until the next HH:MM, in 1..86400.
seconds_until() {
  local h="${1%%:*}" m="${1##*:}" H M S
  read -r H M S <<< "$(date +'%H %M %S')"
  echo $(((10#$h * 3600 + 10#$m * 60 - (10#$H * 3600 + 10#$M * 60 + 10#$S) + 86399) % 86400 + 1))
}

snooze() {
  sleep "$1" &
  SLEEP_PID=$!
  wait "$SLEEP_PID"
}

schedule() {
  trap 'kill "${SLEEP_PID:-}" 2>/dev/null; exit 0' TERM INT
  local reason s
  reason="$(unconfigured_reason)"
  if [ -n "$reason" ]; then
    set_status unconfigured
    while :; do
      log "NOT CONFIGURED ($reason): NO PANEL BACKUPS ARE BEING TAKEN. Set BACKUP_DEST and BACKUP_AGE_RECIPIENTS in .env.production; see docs/DEPLOYMENT.md section 9."
      snooze 86400
    done
  fi
  run_recorded check || log "ERROR: backups are configured but cannot run yet; fix the error above. Retrying at $BACKUP_TIME."
  while :; do
    s="$(seconds_until "$BACKUP_TIME" 2>/dev/null)" || s=3600
    log "next backup in ${s}s (BACKUP_TIME=$BACKUP_TIME, container clock $(date +%H:%M\ %Z))"
    snooze "$s"
    if ! once; then
      log "retrying once in an hour"
      snooze 3600
      once || true
    fi
  done
}

decrypt() {
  local archive="$1" identity="$2" into="$3"
  [ -r "$archive" ] || die "cannot read backup archive $archive"
  [ -r "$identity" ] || die "cannot read age identity file $identity"
  age --decrypt -i "$identity" "$archive" | tar -x -C "$into" -f - ||
    die "cannot decrypt $archive with that identity (wrong key, or a damaged file)"
  [ -s "$into/db.dump" ] && [ -s "$into/panel-data.tar" ] || die "$archive is not a panel backup"
}

restore() {
  [ $# -ge 2 ] || die "usage: panel-backup restore ARCHIVE IDENTITY [--force]"
  local archive="$1" identity="$2" force="${3:-}" tables
  umask 077
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  decrypt "$archive" "$identity" "$WORK"
  log "backup manifest:"
  sed 's/^/    /' "$WORK/MANIFEST"
  mkdir "$WORK/data"
  tar -x -C "$WORK/data" -f "$WORK/panel-data.tar"
  [ -s "$WORK/data/secrets.json" ] || die "the backup holds no secrets.json"
  pg_restore --list "$WORK/db.dump" > /dev/null ||
    die "this pg_restore cannot read the dump (damaged, or taken by a newer PostgreSQL than $(pg_restore --version))"
  load_db_password

  # Every refusal happens before anything is changed.
  tables="$(psql -X -Atc "select count(*) from pg_tables where schemaname not in ('pg_catalog','information_schema')")"
  if [ "$force" != --force ]; then
    [ "$tables" = 0 ] ||
      die "database ${PGDATABASE:-} already holds $tables tables. A restore REPLACES them. Stop the panel and rerun with --force only if that is what you want."
    if [ -e "$PANEL_DATA_DIR/secrets.json" ] && ! cmp -s "$WORK/data/secrets.json" "$PANEL_DATA_DIR/secrets.json"; then
      die "$PANEL_DATA_DIR already holds a different secrets.json (a panel has booted here). Rerun with --force to replace it; the old file is kept beside it."
    fi
  fi

  : "${PGDATABASE:?PGDATABASE is required}"
  if [ "$tables" != 0 ]; then
    log "replacing database $PGDATABASE (--force)"
    dropdb --if-exists --force "$PGDATABASE"
    createdb "$PGDATABASE"
  fi
  pg_restore --no-owner --no-acl --single-transaction --exit-on-error --dbname="$PGDATABASE" "$WORK/db.dump"

  mkdir -p "$PANEL_DATA_DIR"
  if [ -e "$PANEL_DATA_DIR/secrets.json" ] && ! cmp -s "$WORK/data/secrets.json" "$PANEL_DATA_DIR/secrets.json"; then
    cp -p "$PANEL_DATA_DIR/secrets.json" "$PANEL_DATA_DIR/secrets.json.replaced-$(date -u +%Y%m%dT%H%M%SZ)"
  fi
  tar -x -C "$PANEL_DATA_DIR" -f "$WORK/panel-data.tar"
  cmp -s "$WORK/data/secrets.json" "$PANEL_DATA_DIR/secrets.json" || die "secrets.json did not restore intact"
  # The archive is a backup of exactly this database: a newer panel with
  # MIGRATION_REQUIRE_BACKUP_WITHIN may migrate it.
  psql -X -q -c "INSERT INTO system_settings (section, data, updated_at) VALUES ('pre_migration_backup',
    jsonb_build_object('takenAt', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), 'file', 'restored from $(basename "$archive" | tr -cd 'A-Za-z0-9._-')'), NOW())
    ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()" > /dev/null 2>&1 || true

  log "restore ok: database ${PGDATABASE} (last migration $(psql -X -Atc 'select max(version) from schema_migrations' 2>/dev/null || echo unknown), $(psql -X -Atc 'select count(*) from users' 2>/dev/null || echo '?') users, $(psql -X -Atc 'select count(*) from nodes' 2>/dev/null || echo '?') nodes, $(psql -X -Atc 'select count(*) from vms' 2>/dev/null || echo '?') VMs) and secrets.json"
}

# Puts back .env.production and deploy/secrets on a fresh host; never overwrites.
host_config() {
  [ $# -eq 3 ] || die "usage: panel-backup host-config ARCHIVE IDENTITY DIR"
  local archive="$1" identity="$2" dir="$3" f rel
  umask 077
  WORK="$(mktemp -d)"
  trap 'rm -rf "$WORK"' EXIT
  decrypt "$archive" "$identity" "$WORK"
  [ -d "$WORK/host-config" ] || die "the backup holds no host configuration"
  (cd "$WORK/host-config" && find . -type f) | while read -r f; do
    rel="${f#./}"
    if [ -e "$dir/$rel" ]; then
      log "kept existing $rel"
    else
      mkdir -p "$(dirname "$dir/$rel")"
      cp "$WORK/host-config/$rel" "$dir/$rel"
      log "restored $rel"
    fi
  done
}

healthcheck() {
  case "$(cat "$STATUS_FILE" 2>/dev/null || true)" in failed*) exit 1 ;; esac
}

cmd="${1:-schedule}"
[ $# -eq 0 ] || shift
case "$cmd" in
  schedule) schedule ;;
  once) once ;;
  check) check ;;
  _backup) backup ;;
  restore) restore "$@" ;;
  host-config) host_config "$@" ;;
  healthcheck) healthcheck ;;
  *) die "unknown command '$cmd' (schedule, once, check, healthcheck, restore, host-config)" ;;
esac
