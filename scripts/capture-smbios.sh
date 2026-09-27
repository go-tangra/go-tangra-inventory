#!/usr/bin/env bash
# Capture the raw SMBIOS table of THIS host as a decoder test fixture
# (feature 023). Writes <out>/<name>.bin (the DMI structure table),
# <out>/<name>.ep (the entry point) and, when dmidecode is installed,
# <out>/<name>.dmidecode.txt (types 0,1,2,3,4,16,17) as a human oracle.
#
#   sudo scripts/capture-smbios.sh <name> [internal/agentfacts/testdata/smbios]
#
# Serial numbers, UUIDs and asset tags are real host data: replace them with
# synthetic values of equal length before committing a capture (the decoder
# tests only need the structure layout). Never run this on a production host
# without the owner's consent.
set -euo pipefail
name="${1:?usage: capture-smbios.sh <name> [out-dir]}"
out="${2:-internal/agentfacts/testdata/smbios}"
if [[ ! "$name" =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]]; then
  echo "capture-smbios: name must match ^[a-z0-9][a-z0-9-]{0,62}$" >&2
  exit 1
fi
dmi=/sys/firmware/dmi/tables
if [[ ! -r "$dmi/DMI" ]]; then
  echo "capture-smbios: $dmi/DMI is not readable (run as root on Linux)" >&2
  exit 1
fi
mkdir -p "$out"
cp "$dmi/DMI" "$out/$name.bin"
cp "$dmi/smbios_entry_point" "$out/$name.ep"
if command -v dmidecode >/dev/null; then
  dmidecode -t 0,1,2,3,4,16,17 > "$out/$name.dmidecode.txt"
fi
chmod 0644 "$out/$name".*
echo "capture-smbios: wrote $out/$name.bin ($(stat -c %s "$out/$name.bin") bytes); scrub serials before committing"
