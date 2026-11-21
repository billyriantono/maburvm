#!/usr/bin/env bash
# Upgrade the panel and web with a way back (docs/DEPLOYMENT.md, Upgrading the panel).
#
# Run from the install directory (docker-compose.production.yml and .env.production):
#
#   scripts/upgrade-panel.sh --version v1.3.0   switch to that published release
#   scripts/upgrade-panel.sh                    pull the release .env.production names again
#   scripts/upgrade-panel.sh --build            build panel and web from this directory
#
#   --yes                  if a rollback needs the database restored, do it without asking
#   --no-restore           on rollback, put the old images back but leave the database alone
#   --skip-offsite-backup  skip the panel-backup run even when panel-backup is configured
#   --timeout SECONDS      how long the new panel has to become healthy (default 1800)
#   --keep N               pre-upgrade dumps kept in backups/ (default 5)
#   --drain-timeout SECONDS  how long running jobs have to finish first (default 900)
#   --no-drain             do not wait for running jobs (the restart cancels them;
#                          they are retried after it)
#
# First an off-host backup (when panel-backup is configured). After the pull or
# build, maintenance mode goes on (customers and the billing system wait, new
# jobs are held) and running jobs get --drain-timeout to finish; if some are
# still running then, maintenance is switched off and nothing else changes.
# Then the panel is stopped and the database dumped into backups/ and recorded,
# which a panel with MIGRATION_REQUIRE_BACKUP_WITHIN set needs before it migrates.
# Maintenance ends once the new panel is healthy. If the new panel or web does
# not become healthy, the previous images are put back and, when migrations ran,
# the dump is restored (asked first, unless --yes).
set -euo pipefail

usage() {
  sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}
log() { printf '==> %s\n' "$*"; }
die() {
  printf 'upgrade-panel: %s\n' "$*" >&2
  exit 1
}

VERSION="" BUILD="" YES="" NO_RESTORE="" SKIP_OFFSITE="" TIMEOUT=1800 KEEP=5 DRAIN_TIMEOUT=900 NO_DRAIN=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift ;;
    --build) BUILD=1 ;;
    --yes) YES=1 ;;
    --no-restore) NO_RESTORE=1 ;;
    --skip-offsite-backup) SKIP_OFFSITE=1 ;;
    --timeout) TIMEOUT="${2:-}"; shift ;;
    --keep) KEEP="${2:-}"; shift ;;
    --drain-timeout) DRAIN_TIMEOUT="${2:-}"; shift ;;
    --no-drain) NO_DRAIN=1 ;;
    -h | --help) usage ;;
    *) echo "unknown option $1" >&2; usage ;;
  esac
  shift
done
[[ "$TIMEOUT" =~ ^[0-9]+$ && "$KEEP" =~ ^[1-9][0-9]*$ && "$DRAIN_TIMEOUT" =~ ^[0-9]+$ ]] ||
  die "--timeout, --keep and --drain-timeout take whole numbers (--keep at least 1)"
[ -z "$VERSION" ] || [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || die "--version takes a release tag such as v1.3.0"
[ -z "$VERSION" ] || [ -z "$BUILD" ] || die "--version and --build cannot be used together"

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ENV_FILE=.env.production
[ -f docker-compose.production.yml ] && [ -f "$ENV_FILE" ] ||
  die "run this in an install directory: docker-compose.production.yml and $ENV_FILE must be here"

compose() { docker compose -f docker-compose.production.yml --env-file "$ENV_FILE" "$@"; }
state() { docker inspect -f "{{$1}}" "$2" 2>/dev/null || true; }

# The last KEY=value in the env file, quotes removed.
env_get() {
  local v
  v="$(sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1)"
  v="${v%\"}" v="${v#\"}" v="${v%\'}" v="${v#\'}"
  printf '%s' "$v"
}
# Replaces KEY=... (or appends it) and keeps the file's mode.
env_set() {
  local tmp
  tmp="$(mktemp "$ENV_FILE.XXXXXX")"
  awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { if (!done) print k "=" v; done = 1; next } { print } END { if (!done) print k "=" v }' "$ENV_FILE" > "$tmp"
  chmod --reference="$ENV_FILE" "$tmp" 2>/dev/null || chmod 600 "$tmp"
  mv "$tmp" "$ENV_FILE"
}

# Waits for a container's health check; fails early if it stops or restarts.
wait_healthy() {
  local c="$1" deadline=$((SECONDS + $2)) st
  while [ "$SECONDS" -lt "$deadline" ]; do
    st="$(state .State.Health.Status "$c")"
    [ "$st" = healthy ] && return 0
    if [ "$(state .State.Status "$c")" != running ] || [ "$(state .RestartCount "$c")" != 0 ]; then
      echo "$c stopped or restarted: it exits when it cannot start" >&2
      return 1
    fi
    sleep 5
  done
  echo "$c did not become healthy within $2 seconds" >&2
  return 1
}

PGUSER_="$(env_get POSTGRES_USER)" PGDB_="$(env_get POSTGRES_DB)"
PGUSER_="${PGUSER_:-maburvm}" PGDB_="${PGDB_:-maburvm}"
psql_q() { compose exec -T postgres psql -X -At -v ON_ERROR_STOP=1 -U "$PGUSER_" -d "$1" -c "$2"; }

command -v docker > /dev/null || die "docker is not installed"
docker compose version > /dev/null 2>&1 || die "the docker compose plugin is missing"
[ "$(state .State.Running maburvm-postgres)" = true ] || die "PostgreSQL (maburvm-postgres) is not running; start the stack first"
OLD_PANEL_ID="$(state .Image maburvm-panel)" OLD_PANEL_REF="$(state .Config.Image maburvm-panel)"
OLD_WEB_ID="$(state .Image maburvm-web)" OLD_WEB_REF="$(state .Config.Image maburvm-web)"
[ -n "$OLD_PANEL_ID" ] || die "there is no panel container (maburvm-panel) to upgrade. A new install is scripts/install-panel.sh"

PREFIX="$(env_get MABURVM_IMAGE_PREFIX)"
if [ -n "$BUILD" ]; then
  [ -z "$PREFIX" ] || die "--build would overwrite the published image tag; this install runs published releases (MABURVM_IMAGE_PREFIX is set)"
else
  [ -n "$PREFIX" ] || die "this install builds its images here (MABURVM_IMAGE_PREFIX is not set): use --build, or set MABURVM_IMAGE_PREFIX to run published releases"
fi

TS="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p backups
chmod 700 backups
cp -p "$ENV_FILE" "backups/env.production.$TS"
# Kept under fixed names so the previous build survives a rebuild of :local.
docker tag "$OLD_PANEL_ID" maburvm-panel:rollback
[ -z "$OLD_WEB_ID" ] || docker tag "$OLD_WEB_ID" maburvm-web:rollback
MIG_BEFORE="$(psql_q "$PGDB_" 'SELECT count(*) FROM schema_migrations')" || die "cannot read schema_migrations"
LAST_BEFORE="$(psql_q "$PGDB_" 'SELECT max(version) FROM schema_migrations')"

if [ -z "$SKIP_OFFSITE" ] && [ -n "$(env_get BACKUP_DEST)" ] && [ -n "$(env_get BACKUP_AGE_RECIPIENTS)" ]; then
  log "Off-host backup (panel-backup once)"
  compose exec -T panel-backup panel-backup once ||
    die "the off-host backup failed, so nothing was changed. See: docker compose -f docker-compose.production.yml --env-file $ENV_FILE logs panel-backup (or rerun with --skip-offsite-backup)"
fi

restore_env() { cp -p "backups/env.production.$TS" "$ENV_FILE"; }

# Maintenance mode, when this run switched it on. The running panel switches it
# off through its own command, so its job queue resumes at once; a stopped one
# is switched off in the database, which the next panel to start reads. Only a
# binary that carries the command may be given it: an older one would start a
# second panel (with its own job workers). The running binary is asked, not the
# database, which is migrated past the binary after a rollback without restore.
MAINT=""
panel_has_maintenance() {
  compose exec -T panel grep -q 'usage: maintenance on' /app/panel > /dev/null 2>&1
}
maint_off_db() {
  [ -n "$MAINT" ] || return 0
  psql_q "$PGDB_" "DELETE FROM system_settings WHERE section = 'maintenance'; UPDATE river_queue SET paused_at = NULL WHERE paused_at IS NOT NULL" > /dev/null ||
    log "WARNING: maintenance mode could not be switched off; switch it off under System settings or with: docker compose ... exec panel /app/panel maintenance off"
  MAINT=""
}
maint_off_running() {
  [ -n "$MAINT" ] || return 0
  if compose exec -T panel /app/panel maintenance off > /dev/null; then
    MAINT=""
  else
    log "WARNING: maintenance mode could not be switched off; switch it off under System settings or with: docker compose ... exec panel /app/panel maintenance off"
  fi
}
# A run that stops part-way leaves maintenance off: through the panel while it
# runs, in the database otherwise.
maint_cleanup() {
  [ -n "$MAINT" ] || return 0
  if [ "$(state .State.Running maburvm-panel)" = true ] && panel_has_maintenance; then
    maint_off_running
  else
    maint_off_db
  fi
}
trap maint_cleanup EXIT
# The env file and the image tags as they were (a build moves :local).
put_back() {
  restore_env
  docker tag maburvm-panel:rollback "$OLD_PANEL_REF" || log "WARNING: could not tag the previous panel image as $OLD_PANEL_REF"
  [ -z "$OLD_WEB_ID" ] || docker tag maburvm-web:rollback "$OLD_WEB_REF" || log "WARNING: could not tag the previous web image as $OLD_WEB_REF"
}

if [ -n "$VERSION" ]; then
  log "Switching to $VERSION"
  env_set MABURVM_VERSION "$VERSION"
fi
if [ -n "$BUILD" ]; then
  # A host without .git gets its stamp from the caller (scripts/deploy-panel.sh exports these).
  GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse HEAD 2> /dev/null || true)}"
  APP_VERSION="${APP_VERSION:-$(git describe --tags --always --dirty 2> /dev/null || true)}"
  BUILD_TIME="${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
  export GIT_COMMIT APP_VERSION BUILD_TIME
  log "Building panel and web ${APP_VERSION:-(unstamped)}"
  compose build panel web || { put_back; die "the build failed; the running panel was not touched"; }
else
  log "Pulling panel and web $(env_get MABURVM_VERSION)"
  compose pull panel web || { restore_env; die "the pull failed; the running panel was not touched"; }
fi

# Running jobs (a backup, a move) would be cancelled by the restart: hold new
# ones and let the running ones finish first.
if [ -n "$NO_DRAIN" ]; then
  log "Not waiting for running jobs (--no-drain): the restart cancels them, and they are retried after it"
elif ! panel_has_maintenance; then
  log "This panel predates maintenance mode: jobs running now are cancelled by the restart, and retried after it"
else
  log "Maintenance mode on: customers and the billing system wait, new jobs are held (running ones get ${DRAIN_TIMEOUT}s)"
  MAINT=1
  set +e
  compose exec -T panel /app/panel maintenance on --hold-jobs --wait "$DRAIN_TIMEOUT" \
    --message "The panel is being upgraded. Changes are paused for a few minutes; your servers keep running."
  rc=$?
  set -e
  case "$rc" in
    0) ;;
    3)
      maint_off_running
      put_back
      die "jobs were still running after ${DRAIN_TIMEOUT}s (listed above); maintenance was switched off and nothing else changed. Wait for them (Job queue page), or rerun with a longer --drain-timeout or --no-drain"
      ;;
    *)
      maint_cleanup
      put_back
      die "maintenance mode could not be switched on; nothing else changed (or rerun with --no-drain)"
      ;;
  esac
fi

# Dumped with the panel stopped and only after the slow build or pull, so a
# restore loses nothing the current version wrote (a VM a billing system
# created a minute ago, say), only what the new version did.
log "Stopping the panel for the dump (web stays up)"
compose stop panel || { put_back; die "could not stop the panel for the dump; nothing else changed"; }
DUMP="backups/pre-upgrade-$TS.dump"
log "Dumping the database to $DUMP"
if ! (umask 077 && compose exec -T postgres pg_dump -U "$PGUSER_" -d "$PGDB_" --format=custom > "$DUMP") || [ ! -s "$DUMP" ] ||
  ! compose exec -T postgres pg_restore --list < "$DUMP" > /dev/null; then
  rm -f "$DUMP"
  put_back
  maint_off_db
  compose start panel || true
  die "the database dump failed or does not read back; the previous panel was started again and nothing else changed"
fi
find backups -maxdepth 1 -name 'pre-upgrade-*.dump' | sort -r | tail -n +"$((KEEP + 1))" | while read -r old; do rm -f -- "$old"; done
psql_q "$PGDB_" "INSERT INTO system_settings (section, data, updated_at) VALUES ('pre_migration_backup',
  jsonb_build_object('takenAt', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), 'file', '$DUMP'), NOW())
  ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()" > /dev/null ||
  log "WARNING: the dump was not recorded in the database; a panel with MIGRATION_REQUIRE_BACKUP_WITHIN will refuse to migrate"

restore_dump() {
  local lower restoring replaced
  lower="$(printf '%s' "$TS" | tr '[:upper:]' '[:lower:]')"
  restoring="${PGDB_}_restoring_$lower" replaced="${PGDB_}_replaced_$lower"
  log "Restoring $DUMP into a new database, then swapping it in (the current one is kept as $replaced)"
  compose exec -T postgres createdb -U "$PGUSER_" "$restoring" || return 1
  if ! compose exec -T postgres pg_restore -U "$PGUSER_" -d "$restoring" --no-owner --no-acl --single-transaction --exit-on-error < "$DUMP"; then
    compose exec -T postgres dropdb -U "$PGUSER_" --if-exists "$restoring" || true
    return 1
  fi
  psql_q postgres "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$PGDB_' AND pid <> pg_backend_pid()" > /dev/null &&
    psql_q postgres "ALTER DATABASE \"$PGDB_\" RENAME TO \"$replaced\"" &&
    psql_q postgres "ALTER DATABASE \"$restoring\" RENAME TO \"$PGDB_\"" &&
    log "Restored. When the panel is confirmed good, drop the copy: docker compose ... exec postgres dropdb -U $PGUSER_ $replaced"
}

rollback() {
  log "The new version did not come up. Last lines of the panel log:"
  compose logs --no-color --tail 60 panel || true
  local after ran restore=""
  after="$(psql_q "$PGDB_" 'SELECT count(*) FROM schema_migrations' 2> /dev/null || echo "$MIG_BEFORE")"
  ran=$((after - MIG_BEFORE))
  put_back

  if [ "$ran" -gt 0 ]; then
    log "$ran migration(s) ran; the database was at $LAST_BEFORE before the upgrade."
    if [ -n "$NO_RESTORE" ]; then
      :
    elif [ -n "$YES" ]; then
      restore=1
    elif [ -t 0 ]; then
      read -r -p "Restore the database from $DUMP? What the new version wrote is lost. [y/N] " answer
      case "$answer" in y | Y | yes) restore=1 ;; esac
    fi
    if [ -n "$restore" ]; then
      compose stop panel web || true
      restore_dump || log "ERROR: the restore failed; the database is as the new version left it. Restore by hand (docs/DEPLOYMENT.md, Rolling back)."
    else
      log "WARNING: the database was not restored. The previous panel now runs on a schema migrated past it; most migrations add, so it often works, but check. Restore later from $DUMP (docs/DEPLOYMENT.md, Rolling back)."
    fi
  fi

  # The :rollback tags by name, whatever .env.production says: an env file
  # edited before this run (MABURVM_IMAGE_PREFIX/VERSION) would otherwise start
  # the failed version again.
  # The previous panel starts out of maintenance, also from a restored dump
  # (it was taken in maintenance).
  compose stop panel > /dev/null 2>&1 || true
  maint_off_db
  log "Starting the previous panel and web (maburvm-panel:rollback, maburvm-web:rollback)"
  MABURVM_IMAGE_PREFIX=maburvm MABURVM_VERSION=rollback compose up -d --no-deps --no-build --force-recreate panel || true
  if wait_healthy maburvm-panel "$TIMEOUT"; then
    [ -z "$OLD_WEB_ID" ] || { MABURVM_IMAGE_PREFIX=maburvm MABURVM_VERSION=rollback compose up -d --no-deps --no-build --force-recreate web && wait_healthy maburvm-web 300; } || true
    local p v want
    p="$(env_get MABURVM_IMAGE_PREFIX)" v="$(env_get MABURVM_VERSION)"
    want="${p:-maburvm}-panel:${v:-local}"
    [ "$want" = "$OLD_PANEL_REF" ] ||
      log "WARNING: $ENV_FILE names $want, not the $OLD_PANEL_REF that ran before. Fix it before the next docker compose up, which would start $want."
    log "Rolled back to $OLD_PANEL_REF. The upgrade did not happen."
  else
    log "ERROR: the previous panel did not come back either. Look at its log; the dump is $DUMP."
  fi
  exit 1
}

log "Starting the new panel; pending migrations run now (up to ${TIMEOUT}s)"
if compose up -d --no-deps --no-build panel && wait_healthy maburvm-panel "$TIMEOUT" &&
  compose up -d --no-deps --no-build web && wait_healthy maburvm-web 300; then
  maint_off_running
  log "Upgrade done: $(state .Config.Image maburvm-panel), migrations $LAST_BEFORE -> $(psql_q "$PGDB_" 'SELECT max(version) FROM schema_migrations')"
  echo "The previous images stay tagged maburvm-panel:rollback and maburvm-web:rollback, and the dump is $DUMP,"
  echo "until the next upgrade. Rolling back by hand: docs/DEPLOYMENT.md, Rolling back."
else
  rollback
fi
