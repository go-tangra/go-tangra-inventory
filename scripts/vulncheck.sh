#!/usr/bin/env bash
# govulncheck gate. Exactly one advisory is allow-listed, with a hard expiry:
#
#   GO-2026-5471 (GHSA-jj45-xvq5-rhh9, CVE-2026-6993): Kratos transport/http falls
#   back to http.DefaultServeMux. Mitigated in the go-tangra platform transport/http.NewServer, which always
#   installs explicit 404/405 handlers; asserted by the platform tests/contract TestHTTPServerNeverServesDefaultMux.
#   Upstream fix PR go-kratos/kratos#3814 unmerged as of 2026-09-15.
set -euo pipefail
ALLOW="GO-2026-5471"
EXPIRY="2026-12-31"
if [[ "$(date +%F)" > "$EXPIRY" ]]; then
  echo "vulncheck: allow-list for $ALLOW expired on $EXPIRY; re-review upstream status and renew or remove" >&2
  exit 1
fi
if ! command -v govulncheck >/dev/null; then
  echo "vulncheck: installing govulncheck" >&2
  go install golang.org/x/vuln/cmd/govulncheck@latest
fi
# Text mode lists only vulnerabilities that are actually reachable from this module.
out=$(govulncheck ./... 2>&1 || true)
findings=$(printf '%s\n' "$out" | grep -oE '^Vulnerability #[0-9]+: GO-[0-9]+-[0-9]+' | grep -oE 'GO-[0-9]+-[0-9]+' | sort -u || true)
fail=0
for id in $findings; do
  if [[ "$id" == "$ALLOW" ]]; then
    echo "vulncheck: $id present (allow-listed until $EXPIRY, mitigated in code)"
  else
    echo "vulncheck: unallowed vulnerability $id" >&2
    printf '%s\n' "$out" | grep -A4 "$id" >&2 || true
    fail=1
  fi
done
[[ -z "$findings" ]] && echo "vulncheck: no reachable vulnerabilities"
exit $fail
