#!/usr/bin/env bash
# Capture the block-device facts the disk collector reads (feature 023) into
# a directory tree usable as internal/agentfacts/testdata/sysblock/<name>:
# for every /sys/block/<dev> the attribute files size, removable,
# queue/rotational, device/{model,vendor,serial,vpd_pg80}, the resolved
# device path (as a file "devpath"), dev (major:minor), slaves/ and the
# partitions' dev/partition files, plus the udev database entries
# /run/udev/data/b<major>:<minor> (ID_SERIAL_SHORT lines only).
#
#   scripts/capture-sysblock.sh <name> [internal/agentfacts/testdata/sysblock]
#
# The output directory must not exist yet. Serial numbers are real host data:
# replace them with synthetic values of equal length before committing.
set -euo pipefail
name="${1:?usage: capture-sysblock.sh <name> [out-dir]}"
base="${2:-internal/agentfacts/testdata/sysblock}"
if [[ ! "$name" =~ ^[a-z0-9][a-z0-9-]{0,62}$ ]]; then
  echo "capture-sysblock: name must match ^[a-z0-9][a-z0-9-]{0,62}$" >&2
  exit 1
fi
out="$base/$name"
if [[ -e "$out" ]]; then
  echo "capture-sysblock: $out exists; choose another name" >&2
  exit 1
fi
mkdir -p "$out/sys/block" "$out/run/udev/data"
copy_attr() { # <src> <dst>
  if [[ -r "$1" && -f "$1" ]]; then head -c 4096 "$1" > "$2" 2>/dev/null || true; fi
}
for devdir in /sys/block/*; do
  dev=$(basename "$devdir")
  d="$out/sys/block/$dev"
  mkdir -p "$d/queue" "$d/device" "$d/slaves"
  for a in size removable dev; do copy_attr "$devdir/$a" "$d/$a"; done
  copy_attr "$devdir/queue/rotational" "$d/queue/rotational"
  for a in model vendor serial vpd_pg80; do copy_attr "$devdir/device/$a" "$d/device/$a"; done
  readlink -f "$devdir" > "$d/devpath"
  for s in "$devdir"/slaves/*; do [[ -e "$s" ]] && : > "$d/slaves/$(basename "$s")"; done
  for p in "$devdir"/"$dev"*; do
    [[ -d "$p" ]] || continue
    pn=$(basename "$p"); mkdir -p "$d/$pn"
    copy_attr "$p/partition" "$d/$pn/partition"; copy_attr "$p/dev" "$d/$pn/dev"
  done
  if [[ -r "$devdir/dev" ]]; then
    mm=$(cat "$devdir/dev")
    if [[ -r "/run/udev/data/b$mm" ]]; then grep '^E:ID_SERIAL_SHORT=' "/run/udev/data/b$mm" > "$out/run/udev/data/b$mm" || true; fi
  fi
done
echo "capture-sysblock: wrote $out; scrub serials before committing"
