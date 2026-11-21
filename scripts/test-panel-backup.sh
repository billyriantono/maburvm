#!/usr/bin/env bash
# Round-trip test of docker/panel-backup/panel-backup.sh: dump + encrypt + upload,
# then decrypt + restore into a second database, and compare the two.
#
# Needs psql/pg_dump/pg_restore/createdb/dropdb, age, age-keygen, rclone, and a
# PostgreSQL the PG* variables reach as a role that may create databases. The
# "off-host" destination is a local directory. It creates and drops its own
# throwaway databases and touches nothing else.
#
#   PGHOST=/tmp scripts/test-panel-backup.sh
#   SRC_DB=some_migrated_db scripts/test-panel-backup.sh   # back up a real schema (read only)
set -euo pipefail

BACKUP="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/docker/panel-backup/panel-backup.sh"
T="$(mktemp -d)"
OWN_SRC="pbk_src_$$"
DST="pbk_dst_$$"
SRC="${SRC_DB:-$OWN_SRC}"
export PGDATABASE=postgres COPYFILE_DISABLE=1
unset BACKUP_DEST BACKUP_AGE_RECIPIENTS BACKUP_HEARTBEAT_URL BACKUP_TIME

cleanup() {
  dropdb --if-exists "$OWN_SRC" 2>/dev/null || true
  dropdb --if-exists "$DST" 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT

n=0
ok() {
  n=$((n + 1))
  echo "ok $n - $1"
}
fail() {
  echo "FAIL - $1" >&2
  [ ! -f "$T/out" ] || sed 's/^/    /' "$T/out" >&2
  exit 1
}
# refuses DESC PATTERN CMD...: the command must fail and say PATTERN.
refuses() {
  local desc="$1" pattern="$2"
  shift 2
  if "$@" > "$T/out" 2>&1; then fail "$desc: succeeded, expected a refusal"; fi
  grep -q -- "$pattern" "$T/out" || fail "$desc: refused without saying '$pattern'"
  ok "$desc"
}
# in_background_until PATTERN CMD...: run CMD until its output shows PATTERN, then stop it.
in_background_until() {
  local pattern="$1" pid
  shift
  "$@" > "$T/out" 2>&1 &
  pid=$!
  for _ in $(seq 1 40); do
    if grep -q -- "$pattern" "$T/out"; then break; fi
    sleep 0.5
  done
  kill "$pid" 2> /dev/null || true
  wait "$pid" 2> /dev/null || true
  grep -q -- "$pattern" "$T/out"
}
# Everything a restore must reproduce: every row, and every schema object by
# name and kind. Expression text is left out because PostgreSQL re-spells some
# CHECK expressions on reload; enum labels are compared by position because a
# reload renumbers their sort keys; \restrict lines carry a random token.
state_of() {
  psql -X -At -d "$1" <<'SQL'
select 'column', table_name, column_name, udt_name, is_nullable, (column_default is not null)::text from information_schema.columns where table_schema = 'public'
union all select 'index', tablename, indexname, '', '', '' from pg_indexes where schemaname = 'public'
union all select 'constraint', conrelid::regclass::text, conname, contype::text, '', '' from pg_constraint where connamespace = 'public'::regnamespace
union all select 'trigger', tgrelid::regclass::text, tgname, '', '', '' from pg_trigger where not tgisinternal
union all select 'function', proname, pg_get_function_identity_arguments(oid), '', '', '' from pg_proc where pronamespace = 'public'::regnamespace
union all select 'enum', t.typname, lpad((row_number() over (partition by e.enumtypid order by e.enumsortorder))::text, 4, '0'), e.enumlabel, '', '' from pg_enum e join pg_type t on t.oid = e.enumtypid
union all select 'extension', extname, extversion, '', '', '' from pg_extension
order by 1, 2, 3, 4;
SQL
  pg_dump --data-only --no-owner "$1" | grep -Ev '^\\(un)?restrict '
}

if [ -z "${SRC_DB:-}" ]; then
  createdb "$OWN_SRC"
  psql -X -q -v ON_ERROR_STOP=1 -d "$OWN_SRC" <<'SQL'
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TYPE user_role AS ENUM ('admin', 'client');
ALTER TYPE user_role ADD VALUE 'reseller' BEFORE 'client';
CREATE TABLE schema_migrations (version TEXT PRIMARY KEY);
INSERT INTO schema_migrations VALUES ('001_init'), ('002_more');
CREATE TABLE users (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), email TEXT UNIQUE NOT NULL, role user_role NOT NULL, two_factor_secret TEXT);
INSERT INTO users (email, role, two_factor_secret) SELECT 'u' || g || '@example.com', 'client', encode(gen_random_bytes(32), 'hex') FROM generate_series(1, 500) g;
CREATE TABLE nodes (id SERIAL PRIMARY KEY, name TEXT);
INSERT INTO nodes (name) VALUES ('node-a'), ('node-b');
CREATE TABLE vms (id SERIAL PRIMARY KEY, node_id INT REFERENCES nodes(id), spec JSONB);
INSERT INTO vms (node_id, spec) SELECT 1 + g % 2, jsonb_build_object('n', g) FROM generate_series(1, 200) g;
SQL
fi
createdb "$DST"

age-keygen -o "$T/identity" 2> /dev/null
age-keygen -o "$T/other-identity" 2> /dev/null
RECIPIENT="$(age-keygen -y "$T/identity")"

mkdir -p "$T/data/sub" "$T/host/deploy/secrets" "$T/remote" "$T/restored"
echo '{"jwt_secret_key":"test-only","aes_key":"test-only"}' > "$T/data/secrets.json"
echo nested > "$T/data/sub/file"
echo 'PANEL_PUBLIC_URL=https://panel.example.com' > "$T/host/.env.production"
echo 'test-db-password' > "$T/host/deploy/secrets/postgres_password"

export BACKUP_STATUS_FILE="$T/status" HOST_DIR="$T/host" RCLONE_CONFIG="$T/rclone.conf" BACKUP_RETENTION_DAYS=7
# The script refuses a plain path, so the local directory is reached through a remote.
printf '[offsite]\ntype = local\n' > "$RCLONE_CONFIG"
DEST="offsite:$T/remote"
bk() { PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$DEST" BACKUP_AGE_RECIPIENTS="$RECIPIENT" bash "$BACKUP" "$@"; }
rs() { PGDATABASE="$DST" PANEL_DATA_DIR="$T/restored" bash "$BACKUP" restore "$@"; }

# --- not configured, and unsafe configurations ---------------------------------
refuses "an unconfigured backup refuses to run" "not configured: BACKUP_DEST is empty" \
  env PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" bash "$BACKUP" once
grep -q '^failed' "$T/status" || fail "a failed run was not recorded as failed"
if bash "$BACKUP" healthcheck; then fail "healthcheck passed after a failed run"; fi
ok "a failed run turns the healthcheck red"

in_background_until 'NOT CONFIGURED (BACKUP_DEST is empty): NO PANEL BACKUPS ARE BEING TAKEN' bash "$BACKUP" schedule ||
  fail "the idle scheduler did not say it is not configured"
grep -q '^unconfigured' "$T/status" || fail "the idle scheduler did not record 'unconfigured'"
ok "the unconfigured scheduler idles and says so"

SECRET_KEY="$(grep '^AGE-SECRET-KEY-' "$T/identity")"
refuses "a private key as recipient is refused" "holds a PRIVATE key" \
  env PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$DEST" BACKUP_AGE_RECIPIENTS="$SECRET_KEY" bash "$BACKUP" once
if grep -q "$SECRET_KEY" "$T/out"; then fail "the private key was echoed to the log"; fi
ok "the refused private key is not echoed"

refuses "a mistyped public key is refused by check, not at the first nightly run" "age does not accept" \
  env PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$DEST" BACKUP_AGE_RECIPIENTS="${RECIPIENT%????}" bash "$BACKUP" check
refuses "a destination that is a path on this host is refused" "must be an rclone remote" \
  env PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$T/remote" BACKUP_AGE_RECIPIENTS="$RECIPIENT" bash "$BACKUP" once

mv "$T/data/secrets.json" "$T/secrets.aside"
refuses "a data directory without secrets.json is refused" "secrets.json is missing" bk once
mv "$T/secrets.aside" "$T/data/secrets.json"
[ -z "$(ls "$T/remote")" ] || fail "a refused run uploaded something"

printf '[nowhere]\ntype = local\n' > "$T/other.conf"
refuses "check fails when the destination cannot be written" "cannot write to the backup destination" \
  env RCLONE_CONFIG="$T/other.conf" PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$DEST" BACKUP_AGE_RECIPIENTS="$RECIPIENT" bash "$BACKUP" check

# --- backup --------------------------------------------------------------------
bk check > "$T/out" 2>&1 || fail "check failed on a good configuration"
[ -s "$T/remote/panel-backup-check.txt" ] || fail "check did not write its probe to the destination"
touch -t 202001010000 "$T/remote/maburvm-panel-20200101T000000Z.tar.age" "$T/remote/unrelated-old-file"
bk once > "$T/out" 2>&1 || fail "backup failed"
grep -q '^ok' "$T/status" || fail "a good run was not recorded as ok"
bash "$BACKUP" healthcheck || fail "healthcheck failed after a good run"
ARCHIVE="$(ls "$T/remote"/maburvm-panel-*.tar.age)"
[ "$(echo "$ARCHIVE" | wc -l | tr -d ' ')" = 1 ] || fail "expected exactly one backup after pruning, got: $ARCHIVE"
[ -e "$T/remote/unrelated-old-file" ] || fail "retention removed a file that is not a panel backup"
ok "backup uploaded; the old backup was pruned and the unrelated file was kept"

head -c 21 "$ARCHIVE" | grep -q '^age-encryption.org/v1' || fail "the archive is not age-encrypted"
if grep -aqE 'PGDMP|test-only|test-db-password|panel.example.com' "$ARCHIVE"; then fail "plaintext found in the archive"; fi
ok "the archive is encrypted"

# --- schedule arithmetic -------------------------------------------------------
SOON="$(date -v+2M +%H:%M 2> /dev/null || date -d '+2 min' +%H:%M)"
# Through env, not bk: the background job must be the script itself so the kill reaches it.
in_background_until 'next backup in' env BACKUP_TIME="$SOON" PGDATABASE="$SRC" PANEL_DATA_DIR="$T/data" BACKUP_DEST="$DEST" \
  BACKUP_AGE_RECIPIENTS="$RECIPIENT" bash "$BACKUP" schedule || fail "the scheduler never announced the next backup"
secs="$(sed -n 's/.*next backup in \([0-9]*\)s.*/\1/p' "$T/out")"
[ -n "$secs" ] && [ "$secs" -ge 1 ] && [ "$secs" -le 120 ] || fail "scheduler computed a wrong wait for $SOON: '${secs}'"
ok "the scheduler waits ${secs}s for a time two minutes away"

# --- restore -------------------------------------------------------------------
refuses "the wrong key cannot decrypt" "cannot decrypt" rs "$ARCHIVE" "$T/other-identity"
[ "$(psql -X -Atc "select count(*) from pg_tables where schemaname = 'public'" -d "$DST")" = 0 ] || fail "a refused restore changed the database"

rs "$ARCHIVE" "$T/identity" > "$T/out" 2>&1 || fail "restore failed"
diff <(state_of "$SRC") <(state_of "$DST") > "$T/out" || fail "the restored database differs from the source"
cmp -s "$T/data/secrets.json" "$T/restored/secrets.json" || fail "secrets.json differs after restore"
cmp -s "$T/data/sub/file" "$T/restored/sub/file" || fail "the panel data directory was not restored completely"
ok "restore reproduces the database and secrets.json exactly"

refuses "a second restore into the populated database is refused" "already holds" rs "$ARCHIVE" "$T/identity"
dropdb "$DST" && createdb "$DST"
echo '{"jwt_secret_key":"a-newer-panel","aes_key":"generated-here"}' > "$T/restored/secrets.json"
refuses "a different secrets.json is not overwritten silently" "different secrets.json" rs "$ARCHIVE" "$T/identity"
[ "$(psql -X -Atc "select count(*) from pg_tables where schemaname = 'public'" -d "$DST")" = 0 ] || fail "a refused restore changed the database"

psql -X -q -d "$DST" -c 'CREATE TABLE left_over (id int)'
rs "$ARCHIVE" "$T/identity" --force > "$T/out" 2>&1 || fail "restore --force failed"
diff <(state_of "$SRC") <(state_of "$DST") > "$T/out" || fail "after --force the database differs from the source"
cmp -s "$T/data/secrets.json" "$T/restored/secrets.json" || fail "secrets.json differs after --force"
ls "$T/restored"/secrets.json.replaced-* > /dev/null 2>&1 || fail "--force did not keep the replaced secrets.json"
ok "restore --force replaces the database and keeps the old secrets.json beside the new one"

# --- host configuration --------------------------------------------------------
mkdir -p "$T/newhost/deploy/secrets"
echo 'already-here' > "$T/newhost/deploy/secrets/postgres_password"
bash "$BACKUP" host-config "$ARCHIVE" "$T/identity" "$T/newhost" > "$T/out" 2>&1 || fail "host-config failed"
cmp -s "$T/host/.env.production" "$T/newhost/.env.production" || fail ".env.production was not restored"
[ "$(cat "$T/newhost/deploy/secrets/postgres_password")" = already-here ] || fail "host-config overwrote an existing file"
ok "host-config restores what is missing and overwrites nothing"

echo "all $n checks passed"
