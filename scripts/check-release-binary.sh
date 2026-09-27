#!/usr/bin/env bash
# Release-artifact gate for agent self-upgrade (feature 023). Release builds
# use -trimpath, which keeps -ldflags out of the build info, so the binaries
# themselves are inspected. Every agent binary given must:
#   - contain the expected release keyring exactly as injected through
#     -X .../internal/agentrelease.productionKeys (AGENT_RELEASE_PUBLIC_KEYS),
#   - contain no development signing key ("dev-<id>:<base64 key>") and no
#     agentdevkey marker,
#   - carry the expected release version (-X main.version).
# deb/rpm packages are built from the same binaries (make agent-release).
#
#   scripts/check-release-binary.sh -keys "<id>:<key>[,...]" -version 4.5.0 dist/agent/inventory-agent-* ...
set -euo pipefail
keys="" version=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -keys) keys="${2:-}"; shift 2 ;;
    -version) version="${2:-}"; shift 2 ;;
    --) shift; break ;;
    -*) echo "check-release-binary: unknown flag $1" >&2; exit 2 ;;
    *) break ;;
  esac
done
if [[ -z "$keys" || -z "$version" || $# -eq 0 ]]; then
  echo "usage: check-release-binary.sh -keys <keyring> -version <version> <agent binary>..." >&2
  exit 2
fi
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo "release-check: '${version}' is not a release version" >&2
  exit 1
fi
if grep -Eq '(^|,)[[:space:]]*dev-' <<<"$keys"; then
  echo "release-check: the expected keyring itself contains a development key" >&2
  exit 1
fi
fail=0
for f in "$@"; do
  ok=1
  if ! grep -qaF -- "$keys" "$f"; then
    echo "release-check: $f: release signing keyring not injected" >&2
    ok=0
  fi
  if grep -qaE 'dev-[a-z0-9-]{0,28}:[A-Za-z0-9+/]{43}=' "$f"; then
    echo "release-check: $f: carries a development signing key" >&2
    ok=0
  fi
  if grep -qaF 'agentdevkey' "$f"; then
    echo "release-check: $f: development build marker" >&2
    ok=0
  fi
  if ! grep -qaF -- "$version" "$f"; then
    echo "release-check: $f: version ${version} not stamped" >&2
    ok=0
  fi
  if [[ $ok -eq 1 ]]; then echo "release-check: $f ok (${version})"; else fail=1; fi
done
exit $fail
