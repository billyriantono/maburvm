#!/bin/sh
# Bounded static test for docker/minio-init.sh using a fake `mc`.
#
# Validates, WITHOUT Docker, the Phase 0 minio-init control flow against the
# documented semantics of minio/minio:RELEASE.2025-09-07T16-13-09Z:
#   * restricted password file resolution (fail closed)
#   * root-identity guard (abort if MINIO_S3_USER == MINIO_ROOT_USER)
#   * immutable policy: `mc admin policy list` success + name absent -> create;
#     name present -> retain (never overwrite); list FAILURE -> abort before create
#     (fail-closed; the old `policy info` exit-code test was fail-open)
#   * user reconcile via `mc admin user add` every run (upsert)
#   * policy attach idempotent
#   * restricted access proof (alias + mc ls / write-delete probe); delete-probe
#     FAILURE must yield nonzero exit (not a warning)
#   * failures remain nonzero (password missing, policy-list failure, delete fail)
#
# The fake `mc` writes its state under $MC_STATE. No grep/sed/awk used.
# NOTE: the driver does NOT use set -e, because several scenarios intentionally
# expect the real script to abort with nonzero; the real docker/minio-init.sh
# keeps `set -eu` itself.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$REPO_ROOT/docker/minio-init.sh"
TMP="$(mktemp -d)"
BIN="$TMP/bin"
STATE="$TMP/state"
mkdir -p "$BIN" "$STATE"
trap 'rm -rf "$TMP"' EXIT

# ---- fake mc ----------------------------------------------------------------
cat > "$BIN/mc" <<'STUB'
#!/bin/sh
# Fake mc for minio-init.sh testing.
STATE="${MC_STATE:-/tmp}"
cmd="$1"; shift
case "$cmd" in
  alias)
    # alias set <name> <endpoint> <user> <pass>
    echo "$1" > "$STATE/alias_${1}_user"; exit 0 ;;
  ls)
    # mc ls <alias>/<bucket>  -> ok if bucket created
    if [ -f "$STATE/bucket" ]; then exit 0; else exit 1; fi ;;
  mb)
    touch "$STATE/bucket"; exit 0 ;;
  cp)
    # mc cp /dev/null <alias>/<bucket>/<probe>
    if [ "${MC_CP_FAIL:-}" = "1" ]; then exit 1; fi
    touch "$STATE/probe_created"; exit 0 ;;
  rm)
    if [ "${MC_RM_FAIL:-}" = "1" ]; then exit 1; fi
    rm -f "$STATE/probe_created"; exit 0 ;;
  admin)
    sub="$1"; shift
    case "$sub" in
      policy)
        verb="$1"; shift
        if [ "$verb" = "list" ]; then
          # List failure mode: abort before any policy data is emitted.
          if [ "${MC_LIST_FAIL:-}" = "1" ]; then echo "mc: policy list failed" >&2; exit 1; fi
          # Present scenario: emit the versioned policy name; otherwise emit a
          # different (unrelated) policy so the script's name scan does not match.
          if [ -f "$STATE/policy_present" ]; then
            echo "maburvm-bucket-rw-v1"
          else
            echo "readonly"
          fi
          exit 0
        elif [ "$verb" = "create" ]; then
          # If already present (shouldn't happen: list proves absent first), fail.
          if [ -f "$STATE/policy_present" ]; then echo "policy exists" >&2; exit 1; fi
          touch "$STATE/policy_present"; exit 0
        fi ;;
      user)
        verb="$1"; shift
        if [ "$verb" = "add" ]; then
          echo "$2" > "$STATE/user_added"; touch "$STATE/user_added_ran"; exit 0
        fi ;;
      attach)
        # policy attach: idempotent, always succeed
        exit 0 ;;
    esac ;;
esac
exit 0
STUB
chmod +x "$BIN/mc"

# ---- helpers ----------------------------------------------------------------
pass=0; fail=0
check() {
  # $1 = description, $2 = 0/1 expected, $3 = actual
  if [ "$2" = "$3" ]; then
    echo "  PASS: $1 (exit=$3)"
    pass=$((pass+1))
  else
    echo "  FAIL: $1 (expected=$2 actual=$3)"
    fail=$((fail+1))
  fi
}

run_init() {
  # $1 = scenario name (unused); runs the real script with current STATE/mc.
  MC_STATE="$STATE" PATH="$BIN:$PATH" \
    MINIO_ENDPOINT=http://minio:9000 MINIO_ROOT_USER=root MINIO_ROOT_PASSWORD=rootpass \
    MINIO_BUCKET=maburvm MINIO_S3_USER=maburvm-app-v1 \
    MINIO_S3_PASSWORD_FILE="$TMP/s3pw" \
    sh -c 'printf "restrictedpass" > "$MINIO_S3_PASSWORD_FILE"; exec sh "'"$SCRIPT"'"' >/dev/null 2>&1
  echo $?
}

reset_state() { find "$STATE" -mindepth 1 -delete 2>/dev/null; }

echo "=== Scenario A: policy ABSENT (list shows 'readonly') -> create (expect 0) ==="
reset_state
rA=$(run_init A)
check "A exit" 0 "$rA"
check "A policy created" 0 "$( [ -f "$STATE/policy_present" ] && echo 0 || echo 1 )"
check "A user added" 0 "$( [ -f "$STATE/user_added" ] && echo 0 || echo 1 )"
check "A probe cleaned" 0 "$( [ ! -f "$STATE/probe_created" ] && echo 0 || echo 1 )"

echo "=== Scenario B: policy PRESENT (list shows name) -> retain, no recreate (expect 0) ==="
reset_state
touch "$STATE/policy_present"
rB=$(run_init B)
check "B exit" 0 "$rB"
check "B policy retained" 0 "$( [ -f "$STATE/policy_present" ] && echo 0 || echo 1 )"
check "B user reconciled" 0 "$( [ -f "$STATE/user_added" ] && echo 0 || echo 1 )"

echo "=== Scenario C: MINIO_S3_USER == root -> abort (expect nonzero) ==="
reset_state
MC_STATE="$STATE" PATH="$BIN:$PATH" \
  MINIO_ENDPOINT=http://minio:9000 MINIO_ROOT_USER=root MINIO_ROOT_PASSWORD=rootpass \
  MINIO_BUCKET=maburvm MINIO_S3_USER=root \
  MINIO_S3_PASSWORD_FILE="$TMP/s3pw" \
  sh -c 'printf "x" > "$MINIO_S3_PASSWORD_FILE"; exec sh "'"$SCRIPT"'"' >/dev/null 2>&1
check "C root-as-restricted abort" 1 "$?"

echo "=== Scenario D: password file missing -> abort (expect nonzero) ==="
reset_state
MC_STATE="$STATE" PATH="$BIN:$PATH" \
  MINIO_ENDPOINT=http://minio:9000 MINIO_ROOT_USER=root MINIO_ROOT_PASSWORD=rootpass \
  MINIO_BUCKET=maburvm MINIO_S3_USER=maburvm-app-v1 \
  sh -c 'exec sh "'"$SCRIPT"'"' >/dev/null 2>&1
check "D missing password abort" 1 "$?"

echo "=== Scenario E: policy LIST failure -> abort before create (expect nonzero) ==="
reset_state
MC_LIST_FAIL=1 MC_STATE="$STATE" PATH="$BIN:$PATH" \
  MINIO_ENDPOINT=http://minio:9000 MINIO_ROOT_USER=root MINIO_ROOT_PASSWORD=rootpass \
  MINIO_BUCKET=maburvm MINIO_S3_USER=maburvm-app-v1 MINIO_S3_PASSWORD_FILE="$TMP/s3pw" \
  sh -c 'printf "x" > "$MINIO_S3_PASSWORD_FILE"; exec sh "'"$SCRIPT"'"' >/dev/null 2>&1
check "E list-failure abort" 1 "$?"
check "E no policy created on list failure" 0 "$( [ ! -f "$STATE/policy_present" ] && echo 0 || echo 1 )"

echo "=== Scenario F: write OK but delete FAILED -> abort nonzero (expect nonzero) ==="
reset_state
MC_RM_FAIL=1 MC_STATE="$STATE" PATH="$BIN:$PATH" \
  MINIO_ENDPOINT=http://minio:9000 MINIO_ROOT_USER=root MINIO_ROOT_PASSWORD=rootpass \
  MINIO_BUCKET=maburvm MINIO_S3_USER=maburvm-app-v1 MINIO_S3_PASSWORD_FILE="$TMP/s3pw" \
  sh -c 'printf "x" > "$MINIO_S3_PASSWORD_FILE"; exec sh "'"$SCRIPT"'"' >/dev/null 2>&1
check "F delete-probe failure abort" 1 "$?"

echo ""
echo "RESULT: pass=$pass fail=$fail"
[ "$fail" = 0 ]
