#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
dist="$root/dist"
stage="$dist/package-stage"

rm -rf "$stage"
mkdir -p "$stage/linux" "$stage/windows" "$stage/amp"
chmod 0755 "$dist/relay-server-linux-amd64"

cp "$dist/relay-server-linux-amd64" "$stage/linux/relay-server"
cp "$root/README.md" "$root/relay.example.json" "$stage/linux/"
cp "$dist/relay-server-windows-amd64.exe" "$stage/windows/relay-server.exe"
cp "$root/README.md" "$root/relay.example.json" "$stage/windows/"
cp "$dist/relay-server-linux-amd64" "$stage/amp/relay-server"
cp "$root/README.md" "$root/relay.example.json" "$stage/amp/"
chmod 0755 "$stage/linux/relay-server" "$stage/amp/relay-server"

tar -C "$stage/linux" -czf "$dist/relay-linux-amd64.tar.gz" .
(cd "$stage/windows" && zip -q -9 -r "$dist/relay-windows-amd64.zip" .)
(cd "$stage/amp" && zip -q -9 -r "$dist/relay-amp-linux-amd64.zip" .)

(cd "$dist" && sha256sum \
  relay-server-linux-amd64 \
  relay-server-windows-amd64.exe \
  relay-linux-amd64.tar.gz \
  relay-windows-amd64.zip \
  relay-amp-linux-amd64.zip > SHA256SUMS)
