#!/usr/bin/env bash
# Rebuild a panel from a panel-backup archive (docs/DEPLOYMENT.md section 9).
#
# Run from the repo root on the host that will run the panel:
#
#   scripts/panel-restore.sh [--rehearsal] [--force] BACKUP.tar.age AGE_IDENTITY_FILE
#
#   --rehearsal  start the restored panel with no route to nodes, mail, DNS or
#                billing (docker-compose.rehearsal.yml), and without panel-backup.
#                Use it on a scratch machine to prove a backup restores.
#   --force      replace a database and secrets.json that already hold data.
#
# It restores .env.production and deploy/secrets where they are missing, the
# database, and the panel data volume (secrets.json), then starts the stack.
set -euo pipefail

usage() {
  sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

REHEARSAL=""
FORCE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --rehearsal) REHEARSAL=1 ;;
    --force) FORCE=--force ;;
    -h | --help) usage ;;
    -*) echo "unknown option $1" >&2; usage ;;
    *) break ;;
  esac
  shift
done
[ $# -eq 2 ] || usage
[ -r "$1" ] || { echo "cannot read backup archive $1" >&2; exit 1; }
[ -r "$2" ] || { echo "cannot read age identity file $2" >&2; exit 1; }
ARCHIVE="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
IDENTITY="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"

cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ -f docker-compose.production.yml ] || { echo "run this from a checkout of the repo" >&2; exit 1; }
IMAGE=maburvm-panel-backup:local

compose() {
  docker compose -f docker-compose.production.yml ${REHEARSAL:+-f docker-compose.rehearsal.yml} --env-file .env.production "$@"
}

# Checked before anything is touched: on a live panel host this must be a no-op.
RUNNING="$(docker inspect -f '{{.State.Running}}' maburvm-panel 2>/dev/null || true)"
if [ "$RUNNING" = true ] && [ -n "$REHEARSAL" ]; then
  # Not even with --force: the rehearsal overlay would also cut a production panel off from its nodes.
  echo "A panel is running on this host. A rehearsal never replaces a running panel: use a scratch machine." >&2
  echo "If this is the scratch machine and an earlier rehearsal is still up, destroy it first (docs/DEPLOYMENT.md section 9.4)." >&2
  exit 1
fi
if [ -z "$FORCE" ] && [ "$RUNNING" = true ]; then
  echo "A panel is running on this host. A restore REPLACES its database and secrets.json." >&2
  echo "If that is what you want, rerun with --force. To rehearse a restore, use another machine." >&2
  exit 1
fi

echo "==> Building the backup image"
docker build -t "$IMAGE" docker/panel-backup

echo "==> Restoring host configuration that is missing (.env.production, deploy/secrets)"
docker run --rm -v "$ARCHIVE:/in/backup.tar.age:ro" -v "$IDENTITY:/in/identity:ro" -v "$PWD:/host" \
  "$IMAGE" host-config /in/backup.tar.age /in/identity /host
[ -f .env.production ] || {
  echo "The backup holds no .env.production. Create it and the secret files (docs/DEPLOYMENT.md sections 3 and 4), then run this again." >&2
  exit 1
}

echo "==> Starting PostgreSQL"
if [ -n "$FORCE" ]; then compose stop panel web panel-backup; fi
compose up -d postgres
# Over TCP: during first initialisation the image runs a socket-only server that is not the final one.
for _ in $(seq 1 60); do
  if compose exec -T postgres pg_isready -q -h 127.0.0.1; then break; fi
  sleep 2
done
compose exec -T postgres pg_isready -q -h 127.0.0.1 || { echo "PostgreSQL did not become ready" >&2; exit 1; }

echo "==> Restoring the database and the panel data volume"
compose run --rm --no-deps -v "$ARCHIVE:/in/backup.tar.age:ro" -v "$IDENTITY:/in/identity:ro" \
  panel-restore /in/backup.tar.age /in/identity ${FORCE:+--force}

if [ -n "$REHEARSAL" ]; then
  echo "==> Starting the rehearsal stack (no route to nodes or the internet, no panel-backup)"
  compose up -d postgres minio minio-init panel web
  cat <<'MSG'

Rehearsal panel is up on this machine's loopback web port. Check it, then record
the date:
  1. Sign in with an account that has two-factor authentication. A code that is
     accepted proves secrets.json matches the database.
  2. The VM, node and user lists match production as of the backup time.
     Nodes show offline here; that is expected.
Then destroy it, volumes included:
  docker compose -f docker-compose.production.yml -f docker-compose.rehearsal.yml --env-file .env.production down -v
and delete from this machine the age identity file, the backup file, and the
production .env.production and deploy/secrets this script restored.
MSG
else
  echo "==> Starting the stack"
  compose up -d
  cat <<'MSG'

Panel restored. Before relying on it:
  1. Make sure the old panel host is off for good. Two panels driving the same
     nodes will fight.
  2. Delete the age identity file from this host; it must not live here.
  3. Point DNS and the TLS reverse proxy at this host (docs/DEPLOYMENT.md section 7).
  4. If this host has a new address, allow it to reach each node's agent port.
  5. Sign in with a two-factor account and confirm every node comes online.
  6. Compare each node's VM list with the panel's before creating VMs: anything
     done after the backup was taken is missing here (docs/DEPLOYMENT.md 9.3).
  7. Check that panel-backup can still upload:
       docker compose -f docker-compose.production.yml --env-file .env.production exec panel-backup panel-backup check
MSG
fi
