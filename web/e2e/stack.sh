#!/usr/bin/env bash
# Starts what the browser tests run against: a throwaway PostgreSQL database,
# the fake node agent, the panel and the web app. Playwright runs this as its
# web server and stops it when the tests end; everything it started, the
# database included, goes with it.
#
# Needs: go, psql/createdb/dropdb reaching a PostgreSQL server through the
# usual PGHOST/PGPORT/PGUSER/PGPASSWORD (default 127.0.0.1:5432, postgres),
# and port 50051 free (the panel dials every node there).
#
#   E2E_WEB_MODE=start  serve a build made with API_BASE_URL=http://127.0.0.1:$E2E_PANEL_PORT (CI)
#   E2E_WEB_MODE=dev    run the dev server (default)
#   E2E_LOG_DIR=dir     copy the panel and fake agent logs there on exit
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
web="$(dirname "$here")"
repo="$(dirname "$web")"

export PGHOST="${PGHOST:-127.0.0.1}" PGPORT="${PGPORT:-5432}" PGUSER="${PGUSER:-postgres}"
db="${E2E_DB:-maburvm_e2e_$$}"
panel_port="${E2E_PANEL_PORT:-18480}"
web_port="${E2E_WEB_PORT:-3480}"
node_token="e2e-node-token"
customer_vm="e2e00000-0000-0000-0000-0000000000e1"

work="$(mktemp -d)"
pids=()
cleanup() {
  for pid in ${pids[@]+"${pids[@]}"}; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  # E2E_LOG_DIR keeps the panel's and the fake agent's logs after the work
  # directory goes (CI uploads them when a test fails).
  if [ -n "${E2E_LOG_DIR:-}" ]; then
    mkdir -p "$E2E_LOG_DIR" && cp "$work"/*.log "$E2E_LOG_DIR"/ 2>/dev/null || true
  fi
  dropdb --if-exists --force "$db" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

echo "e2e: building the panel and the fake agent"
(cd "$repo" && go build -o "$work/panel" ./cmd/panel && go build -o "$work/fake-agent" ./cmd/fake-agent)

dropdb --if-exists "$db"
createdb "$db"

"$work/fake-agent" -listen 127.0.0.1:50051 -token "$node_token" -vms "$customer_vm=running" >"$work/fake-agent.log" 2>&1 &
pids+=($!)

# Run from the work directory so no .env of the developer's is read, and with
# nothing of theirs that points at real services.
(
  cd "$work"
  exec env -u PDNS_API_URL -u PDNS_API_KEY -u PDNS_SERVER_ID \
    DB_HOST="$PGHOST" DB_PORT="$PGPORT" DB_USER="$PGUSER" DB_PASSWORD="${PGPASSWORD:-unused}" DB_NAME="$db" \
    SERVER_HOST=127.0.0.1 SERVER_PORT="$panel_port" ALLOWED_ORIGINS="http://127.0.0.1:$web_port" \
    MABURVM_DATA_DIR="$work/data" \
    S3_ENDPOINT=http://127.0.0.1:9 S3_ACCESS_KEY=e2e S3_SECRET_KEY=e2e-secret S3_BUCKET=e2e \
    ./panel
) >"$work/panel.log" 2>&1 &
pids+=($!)

for _ in $(seq 1 120); do
  curl -fsS "http://127.0.0.1:$panel_port/health" >/dev/null 2>&1 && break
  if ! kill -0 "${pids[1]}" 2>/dev/null; then cat "$work/panel.log"; exit 1; fi
  sleep 0.5
done
curl -fsS "http://127.0.0.1:$panel_port/health" >/dev/null || { cat "$work/panel.log"; exit 1; }
psql -v ON_ERROR_STOP=1 -q -d "$db" -f "$here/seed.sql"
echo "e2e: panel on :$panel_port (log $work/panel.log), database $db"

cd "$web"
export API_BASE_URL="http://127.0.0.1:$panel_port" NEXT_TELEMETRY_DISABLED=1
if [ "${E2E_WEB_MODE:-dev}" = "start" ]; then
  node node_modules/next/dist/bin/next start -H 127.0.0.1 -p "$web_port" &
else
  node node_modules/next/dist/bin/next dev -H 127.0.0.1 -p "$web_port" &
fi
pids+=($!)
wait "${pids[2]}"
