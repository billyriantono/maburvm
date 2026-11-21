#!/usr/bin/env bash
# Draft release notes for TAG from the Conventional Commits since the tag before it.
#
#   scripts/release-notes.sh v1.2.0 [IMAGE_PREFIX]
#
# Prints Markdown: breaking changes, features, fixes, other changes, the
# database migrations the release adds (an upgrade applies them), and the
# install and upgrade commands. The release workflow posts it as a draft.
set -euo pipefail

TAG="${1:?usage: $0 TAG [IMAGE_PREFIX]}"
PREFIX="${2:-ghcr.io/OWNER/maburvm}"
PREV="$(git describe --tags --abbrev=0 "$TAG^" 2> /dev/null || true)"
RANGE="$TAG"
[ -z "$PREV" ] || RANGE="$PREV..$TAG"

# One line per commit: type<TAB>breaking<TAB>scope<TAB>description (sha)
COMMITS="$(
  breaking=" $(git log --no-merges --format='%h' -E --grep='^BREAKING[ -]CHANGE:' "$RANGE" | tr '\n' ' ') "
  re='^([a-z]+)(\(([^)]*)\))?(!)?: (.*)$'
  git log --no-merges --format='%h %s' "$RANGE" | while read -r sha subj; do
    type=other scope="" brk=0 desc="$subj"
    if [[ "$subj" =~ $re ]]; then
      type="${BASH_REMATCH[1]}" scope="${BASH_REMATCH[3]}" desc="${BASH_REMATCH[5]}"
      [ -z "${BASH_REMATCH[4]}" ] || brk=1
    fi
    [[ "$breaking" != *" $sha "* ]] || brk=1
    printf '%s\t%s\t%s\t%s (%s)\n' "$type" "$brk" "$scope" "$desc" "$sha"
  done
)"

# section TITLE BREAKING [TYPE...]: commits with that breaking flag and one of
# the types; "-" as the only type means every type not listed in OTHER.
OTHER="feat fix perf"
section() {
  local title="$1" brk="$2" lines
  shift 2
  lines="$(printf '%s\n' "$COMMITS" | awk -F '\t' -v brk="$brk" -v types=" $* " -v other=" $OTHER " '
    $0 == "" || $2 != brk { next }
    types == " - " ? index(other, " " $1 " ") == 0 : (types == "  " || index(types, " " $1 " ") > 0) {
      if ($3 != "") printf "- **%s:** %s\n", $3, $4; else printf "- %s\n", $4
    }')"
  [ -z "$lines" ] || printf '### %s\n\n%s\n\n' "$title" "$lines"
}

echo "## MaburVM $TAG"
echo
if [ -n "$PREV" ]; then echo "Changes since $PREV."; else echo "First release."; fi
echo
section "Breaking changes" 1
section "Features" 0 feat
section "Fixes" 0 fix
section "Performance" 0 perf
section "Other changes" 0 -

migrations="$(git diff --name-only --diff-filter=A ${PREV:+"$PREV"} "$TAG" -- 'internal/shared/db/migrations/*.up.sql' 2> /dev/null |
  sed 's|.*/||; s|\.up\.sql$||' || true)"
[ -n "$PREV" ] || migrations=""
if [ -n "$migrations" ]; then
  echo "### Database migrations"
  echo
  echo "The panel applies these when it starts. scripts/upgrade-panel.sh dumps the database first."
  echo
  printf '%s\n' "$migrations" | sed 's/^/- /'
  echo
fi

cat << MSG
### Install or upgrade

Images: \`$PREFIX-panel:$TAG\` and \`$PREFIX-web:$TAG\` (linux/amd64, linux/arm64).
Agent binaries, their SHA-256 sums and, when this release was signed, their
signatures are attached below.

New panel host, from the source archive of this tag:

\`\`\`bash
scripts/install-panel.sh --url https://panel.example.com --version $TAG --image-prefix $PREFIX
\`\`\`

Existing panel host running published images:

\`\`\`bash
scripts/upgrade-panel.sh --version $TAG
\`\`\`

Read docs/DEPLOYMENT.md (Upgrading the panel) first: some upgrades cannot be undone
by starting the previous version.
MSG
