#!/usr/bin/env bash
# Vendor noVNC's client into cmd/deskview/web/novnc: core/ (RFB, the
# client) and vendor/ (pako, its zlib), its license and VERSION.
# noVNC's own UI (app/, vnc.html) stays out: deskview's page is its own.
#
#   scripts/update-novnc.sh 1.7.0
#
# Then: go test ./cmd/deskview (every import resolves), and watch a
# display in deskview.
set -euo pipefail

version=${1:?usage: update-novnc.sh VERSION, e.g. 1.7.0}
repo=$(cd "$(dirname "$0")/.." && pwd)
dest=$repo/cmd/deskview/web/novnc
url=https://github.com/novnc/noVNC/archive/refs/tags/v$version.tar.gz

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL "$url" | tar -xz -C "$work" --strip-components=1

rm -rf "$dest"
mkdir -p "$dest"
cp -r "$work/core" "$work/vendor" "$work/LICENSE.txt" "$work/AUTHORS" \
  "$dest/"
echo "$version" >"$dest/VERSION"

echo "noVNC $version: ${dest#"$repo/"}," \
  "$(find "$dest" -type f | wc -l) files"
