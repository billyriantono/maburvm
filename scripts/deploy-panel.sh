#!/usr/bin/env bash
# Deploy the panel (+ web) to a production host, stamped with this checkout's
# version. The host has no .git, so the stamp must be computed here; without it
# the panel reports itself as unstamped and every agent looks out of date.
#
# The panel image also bundles the node agent built from the same commit, which
# System settings -> "Update" pushes to nodes.
#
# Usage: scripts/deploy-panel.sh root@HOST [services...]   (default: panel web)
set -euo pipefail
HOST="${1:?usage: $0 user@host [services...]}"; shift
SERVICES="${*:-panel web}"
DEST=/root/maburvm
cd "$(dirname "${BASH_SOURCE[0]}")/.."

GIT_COMMIT="$(git rev-parse HEAD)"
git diff --quiet HEAD || GIT_COMMIT="${GIT_COMMIT}-dirty"
# vX.Y.Z on a tagged commit, vX.Y.Z-N-gSHA after it, bare SHA before the first tag.
APP_VERSION="$(git describe --tags --always --dirty)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Deploying ${APP_VERSION} (${GIT_COMMIT}) to ${HOST}: ${SERVICES}"

# Each tree to its own destination. Never `rsync --delete` several dirs into the
# repo root: it deletes docker-compose*.yml and .env.production (it has happened).
for d in cmd internal api integrations web/src; do
  rsync -az --delete "$d/" "$HOST:$DEST/$d/"
done
rsync -az go.mod go.sum Dockerfile.panel "$HOST:$DEST/"
# The web build also reads these from web/, outside web/src. Left unsynced, a
# Tailwind or dependency change builds against the host's stale copy.
rsync -az web/package.json web/package-lock.json web/tailwind.config.ts \
  web/next.config.js web/postcss.config.js web/postcss.config.mjs web/tsconfig.json "$HOST:$DEST/web/"

ssh "$HOST" "cd $DEST && export GIT_COMMIT='$GIT_COMMIT' APP_VERSION='$APP_VERSION' BUILD_TIME='$BUILD_TIME' \
  && docker compose -f docker-compose.production.yml --env-file .env.production build $SERVICES \
  && docker compose -f docker-compose.production.yml --env-file .env.production up -d $SERVICES"
