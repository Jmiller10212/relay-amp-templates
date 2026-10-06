#!/bin/sh
set -eu

version="${1:-0.7.1}"
mkdir -p dist

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X main.version=${version}" \
  -o dist/relay-server-linux-amd64 ./cmd/relay-server

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w -X main.version=${version}" \
  -o dist/relay-server-windows-amd64.exe ./cmd/relay-server
