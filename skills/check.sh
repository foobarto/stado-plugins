#!/usr/bin/env bash

set -euo pipefail

go test ./...
go test -race ./...
go vet ./...

first="$(mktemp -d)"
second="$(mktemp -d)"
trap 'rm -rf "$first" "$second"' EXIT
SKILLS_BUILD_DIR="$first" ./build.sh >/dev/null
SKILLS_BUILD_DIR="$second" ./build.sh >/dev/null
cmp "$first/plugin.wasm" "$second/plugin.wasm"
cmp "$first/plugin.manifest.json" "$second/plugin.manifest.json"

if find "$first" "$second" -name '*.sig' -print -quit | grep -q .; then
  echo "development check produced a signature" >&2
  exit 1
fi
