#!/usr/bin/env bash
# Pack every template and rewrite index.json with the real size and checksum.
#
# The checksum is not decoration: the panel refuses an archive whose sha256
# does not match, so a stale value in index.json means nobody can install the
# template. Computing it here rather than by hand is the only way it stays
# right.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v jq >/dev/null || { echo "jq is required"; exit 1; }
command -v sha256sum >/dev/null || { echo "sha256sum is required"; exit 1; }

tmp=$(mktemp)
cp index.json "$tmp"

for dir in templates/*/; do
  [ -d "$dir" ] || continue
  id=$(basename "$dir")
  out="templates/$id.tar.gz"

  # --sort=name and a fixed mtime make the archive REPRODUCIBLE: without
  # them every run produces a different checksum from identical files, and
  # every push looks like a content change.
  tar --sort=name \
      --mtime='UTC 2020-01-01' \
      --owner=0 --group=0 --numeric-owner \
      --exclude='.DS_Store' --exclude='.git*' \
      -czf "$out" -C "$dir" .

  sum=$(sha256sum "$out" | cut -d' ' -f1)
  size=$(stat -c%s "$out" 2>/dev/null || stat -f%z "$out")
  echo "  $id  $(printf '%8d' "$size") bytes  $sum"

  jq --arg id "$id" --arg sum "$sum" --argjson size "$size" \
     --arg archive "$out" '
       .templates = (.templates | map(
         if .id == $id
         then .sha256 = $sum | .size = $size | .archive = $archive
         else . end))' "$tmp" > "$tmp.new" && mv "$tmp.new" "$tmp"
done

jq --arg d "$(date -u +%Y-%m-%d)" '.updated = $d' "$tmp" > index.json
rm -f "$tmp"
echo "index.json updated."

# Catch the mistake that makes a template invisible: an archive on disk with
# no entry in the catalogue, or an entry pointing at an archive that is not
# there.
for f in templates/*.tar.gz; do
  id=$(basename "$f" .tar.gz)
  jq -e --arg id "$id" '.templates[] | select(.id == $id)' index.json >/dev/null \
    || echo "  WARNING: $f has no entry in index.json"
done
jq -r '.templates[].archive' index.json | while read -r a; do
  [ -f "$a" ] || echo "  WARNING: index.json references $a which does not exist"
done
