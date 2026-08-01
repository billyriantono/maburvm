#!/bin/sh
# MaburVM MinIO one-shot initialization (Phase 0 model).
#
# Runs once after MinIO is healthy. Creates the application bucket, an immutable
# versioned bucket-scoped policy, and a RESTRICTED S3 user (maburvm-app-v1) whose
# credentials are handed to the panel. Root credentials stay confined to MinIO +
# this init step and are NEVER passed to the panel.
#
# Fails closed: real errors abort (exit nonzero). Idempotent, safe checks
# (already-exists) are allowed and do NOT trigger failure. No `|| true` / `|| echo`
# masking of real errors. Secret values are never printed.
#
# The selected image includes /bin/sh and mc, but NOT grep/sed/awk. Only POSIX sh
# builtins and mc are used.
set -eu

# Image entrypoint is overridden to /bin/sh and this script is the command, so we
# run as a normal script.

MINIO_ALIAS="myminio"          # root alias (root creds, confined here)
APP_ALIAS="maburvm-app"        # restricted alias used only to prove access
BUCKET="${MINIO_BUCKET:-maburvm}"
S3_USER="${MINIO_S3_USER:-maburvm-app-v1}"
POLICY_NAME="maburvm-bucket-rw-v1"
POLICY_FILE="$(mktemp)"
POLICY_LIST_FILE="$(mktemp)"
trap 'rm -f "$POLICY_FILE" "$POLICY_LIST_FILE"' 0

# --- Restricted S3 password (file-backed, fail closed) ------------------------
# Ordinary nonempty MINIO_S3_PASSWORD may take precedence if deliberately
# supplied; otherwise a readable, non-empty MINIO_S3_PASSWORD_FILE is required.
# The resolved value is exported ONLY for child `mc` and the file var is unset.
# Contents are never echoed.
if [ -n "${MINIO_S3_PASSWORD:-}" ]; then
  : # plain env supplied; use as-is
elif [ -n "${MINIO_S3_PASSWORD_FILE:-}" ]; then
  if [ ! -r "$MINIO_S3_PASSWORD_FILE" ]; then
    echo "minio-init: MINIO_S3_PASSWORD_FILE is not readable: $MINIO_S3_PASSWORD_FILE" >&2
    exit 1
  fi
  _pw="$(cat "$MINIO_S3_PASSWORD_FILE")"
  if [ -z "$_pw" ]; then
    echo "minio-init: MINIO_S3_PASSWORD_FILE is empty: $MINIO_S3_PASSWORD_FILE" >&2
    exit 1
  fi
  MINIO_S3_PASSWORD="$_pw"
  unset MINIO_S3_PASSWORD_FILE
else
  echo "minio-init: MINIO_S3_PASSWORD or MINIO_S3_PASSWORD_FILE is required" >&2
  exit 1
fi
export MINIO_S3_PASSWORD

# --- Guard: restricted user must NOT be the root identity ---------------------
if [ "$S3_USER" = "${MINIO_ROOT_USER:-}" ]; then
  echo "minio-init: REFUSING to use root identity as restricted user ($S3_USER)" >&2
  exit 1
fi

echo "minio-init: configuring root alias ${MINIO_ALIAS} -> ${MINIO_ENDPOINT}"
mc alias set "${MINIO_ALIAS}" "${MINIO_ENDPOINT}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}"

# 1) Bucket (idempotent)
if mc ls "${MINIO_ALIAS}/${BUCKET}" >/dev/null 2>&1; then
  echo "minio-init: bucket ${BUCKET} already exists"
else
  echo "minio-init: creating bucket ${BUCKET}"
  mc mb "${MINIO_ALIAS}/${BUCKET}"
fi

# 2) Immutable versioned bucket-scoped policy.
# Operations are exactly those required by internal/panel/storage/s3.go:
#   s3:ListBucket             -> List / SDK region discovery
#   s3:GetBucketLocation      -> SDK location lookup
#   s3:GetObject              -> Download / HeadObject / presigned GET
#   s3:PutObject              -> Upload (single + multipart parts/complete)
#   s3:DeleteObject           -> Delete
#   s3:AbortMultipartUpload   -> multipart upload abort
#   s3:ListMultipartUploadParts -> multipart part listing
cat > "$POLICY_FILE" <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:GetBucketLocation",
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:AbortMultipartUpload",
        "s3:ListMultipartUploadParts"
      ],
      "Resource": ["arn:aws:s3:::${BUCKET}", "arn:aws:s3:::${BUCKET}/*"]
    }
  ]
}
JSON

# Fail-closed policy reconciliation. `mc admin policy info` exits 1 for BOTH
# "no such policy" and auth/transport/server failures, so testing its exit code
# is fail-open. Instead we LIST policies (stdout only; stderr is diagnostic, never
# parsed as policy data or secrets), abort explicitly if the list itself fails,
# and only create after a successful list proves the policy name is absent.
echo "minio-init: listing policies"
if ! mc admin policy list "${MINIO_ALIAS}" > "$POLICY_LIST_FILE" 2> "$POLICY_LIST_FILE.err"; then
  echo "minio-init: 'mc admin policy list' failed; cannot reconcile safely:" >&2
  cat "$POLICY_LIST_FILE.err" >&2
  exit 1
fi

policy_exists=false
while IFS= read -r _line || [ -n "$_line" ]; do
  [ "$_line" = "$POLICY_NAME" ] && policy_exists=true && break
done < "$POLICY_LIST_FILE"
rm -f "$POLICY_LIST_FILE.err"

if [ "$policy_exists" = true ]; then
  echo "minio-init: policy ${POLICY_NAME} already present (immutable, retained)"
else
  # List succeeded and the exact name scan above proved absence: create unmasked
  # (real errors abort via set -e). No broad error masks.
  echo "minio-init: creating policy ${POLICY_NAME}"
  mc admin policy create "${MINIO_ALIAS}" "${POLICY_NAME}" "$POLICY_FILE"
fi

# 3) Restricted S3 user — reconcile (upsert) on every init.
# `mc admin user add` is a PUT/upsert in this mc generation, so a rotated secret is
# applied deterministically. We do NOT use `mc admin accesskey edit`, and we do NOT
# `user remove`/`user disable` retired identities (non-idempotent / unverified).
echo "minio-init: reconciling restricted user ${S3_USER}"
mc admin user add "${MINIO_ALIAS}" "${S3_USER}" "${MINIO_S3_PASSWORD}"

# 4) Attach the current policy (idempotent in this mc generation).
echo "minio-init: attaching policy ${POLICY_NAME} to ${S3_USER}"
mc admin policy attach "${MINIO_ALIAS}" "${POLICY_NAME}" --user "${S3_USER}"

# 5) Prove real restricted access with a SEPARATE non-root alias.
echo "minio-init: configuring restricted alias ${APP_ALIAS}"
mc alias set "${APP_ALIAS}" "${MINIO_ENDPOINT}" "${S3_USER}" "${MINIO_S3_PASSWORD}"

echo "minio-init: verifying restricted read access to ${APP_ALIAS}/${BUCKET}"
mc ls "${APP_ALIAS}/${BUCKET}"

# Restricted write/delete probe. If the write succeeds but the delete fails, the
# initializer must exit nonzero (not merely warn) so verification is not masked.
# Best-effort cleanup is still attempted, but the failure is not swallowed.
_PROBE="maburvm-init-probe-$$"
if mc cp /dev/null "${APP_ALIAS}/${BUCKET}/${_PROBE}" >/dev/null 2>&1; then
  if mc rm "${APP_ALIAS}/${BUCKET}/${_PROBE}" >/dev/null 2>&1; then
    echo "minio-init: restricted write/delete probe OK"
  else
    echo "minio-init: write probe succeeded but delete FAILED; object ${_PROBE} may remain (clean manually)" >&2
    exit 1
  fi
else
  echo "minio-init: restricted write probe FAILED (bucket access not fully verified)" >&2
  exit 1
fi

echo "minio-init: done"
