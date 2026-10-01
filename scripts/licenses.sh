#!/usr/bin/env bash
# Writes the license texts that must accompany a binary distribution:
#   $1/THIRD-PARTY-LICENSES.txt  every module linked into akari-client (GOOS=$2)
#   $1/mihomo-LICENSE.txt        GPL-3.0 text of the shipped kernel
# Usage: scripts/licenses.sh <out-dir> <goos>
set -euo pipefail
out=$1 goos=$2
mkdir -p "$out"
f="$out/THIRD-PARTY-LICENSES.txt"
{
  echo "akari-client includes the following third-party software."
  echo "See THIRD-PARTY-NOTICES.md in the source for an overview."
  echo
} >"$f"
GOOS=$goos go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}}|{{.Dir}}{{end}}{{end}}' ./cmd/akari-client |
  sort -u | while IFS='|' read -r mod dir; do
    [ -n "$mod" ] || continue
    {
      echo "================================================================"
      echo "$mod"
      echo "================================================================"
      found=0
      for n in LICENSE LICENSE.txt LICENSE.md COPYING NOTICE; do
        if [ -f "$dir/$n" ]; then echo "--- $n"; cat "$dir/$n"; echo; found=1; fi
      done
      [ $found = 1 ] || { echo "license file not found for $mod" >&2; exit 1; }
    } >>"$f"
  done
mdir=$(cd third_party/mihomo && go list -m -f '{{.Dir}}' github.com/metacubex/mihomo)
cp "$mdir/LICENSE" "$out/mihomo-LICENSE.txt"
