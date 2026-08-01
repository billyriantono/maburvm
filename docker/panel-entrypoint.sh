#!/bin/sh
# MaburVM panel container entrypoint.
#
# Runs as ROOT (Dockerfile.panel has no USER directive) so it can:
#   1. guarantee the runtime data dir (/data) is owned by the unprivileged
#      `maburvm` user, then
#   2. read host-provided secret files before privilege drop, then
#   3. exec the panel as `maburvm` via su-exec.
#
# The panel stays non-root at runtime. This is a LOCAL-COMPOSE mitigation, not
# managed secret isolation: source secret files remain host-side bind mounts
# (see docs/DEPLOYMENT.md); phase 1 / operational secret-manager work is separate.
#
# NOTE: secret resolution runs in the PARENT shell (no pipe/subshell) so the
# exported NAME values and unset *_FILE vars reach the final `exec su-exec`.
set -eu

# --- 1. Runtime data dir ownership (restricted, no env-controlled path) ---------
# Only the known data directory is chowned. An operator overriding the path to
# something unexpected is rejected rather than recursively chowning arbitrary
# locations.
DATA_DIR="${MABURVM_DATA_DIR:-/data}"
case "$DATA_DIR" in
  /data|/data/)
    mkdir -p "$DATA_DIR"
    chown -R maburvm:maburvm "$DATA_DIR"
    ;;
  *)
    echo "entrypoint: refusing to chown unexpected MABURVM_DATA_DIR=$DATA_DIR (only /data is supported)" >&2
    exit 1
    ;;
esac

# --- 2. Resolve *_FILE secret inputs before dropping privileges ----------------
# Fixed, explicit resolver. Ordinary non-empty NAME wins; otherwise NAME_FILE
# (if supplied) must be readable and non-empty. Secret contents are never
# printed. We export NAME and unset NAME_FILE in the PARENT shell (this runs at
# top level, NOT in a pipe/subshell) so the values reach `exec su-exec ... panel`.
#
# This is a fixed list of known names — no attacker-controlled name/path iteration.
resolve_secret() {
  _name="$1"
  _filevar="$2"
  eval "_val=\"\${$_name:-}\""
  eval "_fval=\"\${$_filevar:-}\""

  if [ -n "$_val" ]; then
    # Non-empty plain env wins. Ensure the *_FILE variant is not also set.
    unset "$_filevar" 2>/dev/null || true
    return 0
  fi

  if [ -z "$_fval" ]; then
    # Neither provided: leave unset; the app resolver handles defaults/optional.
    return 0
  fi

  if [ ! -r "$_fval" ]; then
    echo "entrypoint: secret file '$_filevar=$_fval' is not readable" >&2
    exit 1
  fi

  _read="$(cat "$_fval")"
  if [ -z "$_read" ]; then
    echo "entrypoint: secret file '$_filevar=$_fval' is empty" >&2
    exit 1
  fi

  # Export NAME with the file contents; unset the FILE var so the app resolver
  # (which checks env first, then trims whitespace/newlines) uses this value.
  export "$_name=$_read"
  unset "$_filevar" 2>/dev/null || true
}

resolve_secret DB_PASSWORD       DB_PASSWORD_FILE
resolve_secret S3_ACCESS_KEY     S3_ACCESS_KEY_FILE
resolve_secret S3_SECRET_KEY     S3_SECRET_KEY_FILE
resolve_secret JWT_SECRET_KEY    JWT_SECRET_KEY_FILE
resolve_secret AES_KEY           AES_KEY_FILE

# --- 3. Drop privileges and exec the panel -------------------------------------
exec su-exec maburvm:maburvm ./panel "$@"
