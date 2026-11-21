#!/usr/bin/env bash
# Exercises scripts/upgrade-panel.sh against a stand-in docker: a good upgrade,
# a failed one whose migrations ran (images and database put back), and a
# failed one that never migrated (images only). Proves the decisions, not Docker.
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail() {
  echo "FAIL: $*" >&2
  exit 1
}

setup() {
  T="$(mktemp -d)"
  mkdir -p "$T/scripts" "$T/bin" "$T/state"
  cp "$SRC/scripts/upgrade-panel.sh" "$T/scripts/"
  : > "$T/docker-compose.production.yml"
  printf 'POSTGRES_USER=maburvm\nPOSTGRES_DB=maburvm\nMABURVM_IMAGE_PREFIX=ghcr.io/example/maburvm\nMABURVM_VERSION=v1.0.0\n' > "$T/.env.production"
  echo 76 > "$T/state/migrations"
  echo 0 > "$T/state/restarts"
  echo healthy > "$T/state/health"
  echo "${1:-ok}" > "$T/state/scenario"
  cat > "$T/bin/docker" << 'FAKE'
#!/usr/bin/env bash
S="$(dirname "$0")/../state"
echo "docker $*${MABURVM_VERSION:+ [MABURVM_VERSION=$MABURVM_VERSION]}" >> "$S/calls"
args="$*"
case "$args" in
  "compose version") exit 0 ;;
  inspect*) ;;
  tag*) exit 0 ;;
  *" exec -T postgres psql "*)
    case "$args" in
      *"count(*) FROM schema_migrations"*) cat "$S/migrations" ;;
      *"max(version)"*) echo "076_last" ;;
    esac
    exit 0 ;;
  *" exec -T postgres pg_dump "*) [ "$(cat "$S/scenario")" != dump-fails ] || exit 1; echo "PGDMP-fake"; exit 0 ;;
  *" exec -T postgres pg_restore "*) cat > /dev/null; exit 0 ;;
  *" exec -T postgres "*) exit 0 ;;
  *" up -d --no-deps --no-build --force-recreate panel"*) echo 0 > "$S/restarts"; echo healthy > "$S/health"; exit 0 ;;
  *" up -d --no-deps --no-build panel"*)
    case "$(cat "$S/scenario")" in
      migrated-then-crashed) echo 78 > "$S/migrations"; echo 1 > "$S/restarts"; echo starting > "$S/health" ;;
      crashed) echo 1 > "$S/restarts"; echo starting > "$S/health" ;;
    esac
    exit 0 ;;
  *) exit 0 ;;
esac
# docker inspect -f '{{X}}' NAME
fmt="$3" name="$4"
case "$fmt:$name" in
  "{{.State.Running}}:maburvm-postgres") echo true ;;
  "{{.Image}}:maburvm-panel") echo sha256:oldpanel ;;
  "{{.Config.Image}}:maburvm-panel") echo ghcr.io/example/maburvm-panel:v1.0.0 ;;
  "{{.Image}}:maburvm-web") echo sha256:oldweb ;;
  "{{.Config.Image}}:maburvm-web") echo ghcr.io/example/maburvm-web:v1.0.0 ;;
  "{{.State.Health.Status}}:"*) cat "$S/health" ;;
  "{{.State.Status}}:"*) echo running ;;
  "{{.RestartCount}}:maburvm-panel") cat "$S/restarts" ;;
  "{{.RestartCount}}:"*) echo 0 ;;
esac
FAKE
  chmod +x "$T/bin/docker"
}

run() { (cd "$T" && PATH="$T/bin:$PATH" bash scripts/upgrade-panel.sh "$@" < /dev/null > "$T/out" 2>&1); }
calls() { cat "$T/state/calls"; }

# A good upgrade: backup taken and recorded, release switched, no rollback.
setup ok
run --version v1.1.0 || { cat "$T/out"; fail "a good upgrade must succeed"; }
grep -q '^MABURVM_VERSION=v1.1.0$' "$T/.env.production" || fail "MABURVM_VERSION was not switched"
ls "$T"/backups/pre-upgrade-*.dump > /dev/null || fail "no pre-upgrade dump"
calls | grep -q "pre_migration_backup" || fail "the dump was not recorded"
calls | grep -q "pull panel web" || fail "the new images were not pulled"
! calls | grep -q "force-recreate" || fail "a good upgrade must not roll back"
# The dump comes after the pull, with the panel stopped, so a restore loses
# nothing the previous version wrote.
order="$(calls | awk '/pull panel web/ { printf "pull " } / stop panel/ { printf "stop " } /pg_dump/ { printf "dump " } /up -d --no-deps --no-build panel/ { printf "up " }')"
[ "$order" = "pull stop dump up " ] || fail "expected pull, stop, dump, start; got: $order"
rm -rf "$T"

# Migrations ran and the panel crashed: images back, database restored (--yes).
setup migrated-then-crashed
if run --version v1.1.0 --yes; then fail "a failed upgrade must exit non-zero"; fi
grep -q '^MABURVM_VERSION=v1.0.0$' "$T/.env.production" || fail "the env file was not put back"
calls | grep -q "tag maburvm-panel:rollback ghcr.io/example/maburvm-panel:v1.0.0" || fail "the old panel image was not re-tagged"
calls | grep -q "createdb -U maburvm maburvm_restoring_" || fail "the dump was not restored into a new database"
calls | grep -qF 'RENAME TO "maburvm_replaced_' || fail "the old database was not kept aside"
calls | grep -q "up -d --no-deps --no-build --force-recreate panel \[MABURVM_VERSION=rollback\]" || fail "the previous panel was not started from its :rollback image"
grep -q "Rolled back" "$T/out" || { cat "$T/out"; fail "no rollback report"; }
rm -rf "$T"

# Crashed before migrating: images back, the database is left alone.
setup crashed
if run --version v1.1.0 --yes; then fail "a failed upgrade must exit non-zero"; fi
! calls | grep -q "createdb" || fail "nothing migrated, so nothing may be restored"
calls | grep -q "force-recreate panel" || fail "the previous panel was not started"
rm -rf "$T"

# The dump fails: the previous panel is started again and nothing else changes.
setup dump-fails
if run --version v1.1.0; then fail "a failed dump must stop the upgrade"; fi
grep -q '^MABURVM_VERSION=v1.0.0$' "$T/.env.production" || fail "the env file was not put back"
calls | grep -q "start panel" || fail "the previous panel was not started again"
! calls | grep -q "up -d --no-deps --no-build panel" || fail "the new panel must not start without a dump"
! ls "$T"/backups/pre-upgrade-*.dump > /dev/null 2>&1 || fail "a failed dump must not be kept"
rm -rf "$T"

# Refusals that change nothing.
setup ok
sed -i.bak '/MABURVM_IMAGE_PREFIX/d' "$T/.env.production"
if run; then fail "a source install without --build must be refused"; fi
! calls | grep -q "pg_dump" || fail "a refused upgrade must not touch the database"
rm -rf "$T"

echo "upgrade-panel.sh: all scenarios passed"
