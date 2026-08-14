#!/usr/bin/env bash
set -euo pipefail

plugin_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
first_build="$(mktemp -d)"
second_build="$(mktemp -d)"
cleanup() {
  rm -rf -- "$first_build" "$second_build"
}
trap cleanup EXIT

cd "$plugin_root"
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test -race ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go vet ./...

TASKS_BUILD_DIR="$first_build" ./build.sh
TASKS_BUILD_DIR="$second_build" ./build.sh
cmp "$first_build/plugin.wasm" "$second_build/plugin.wasm"
cmp "$first_build/plugin.manifest.json" "$second_build/plugin.manifest.json"

if find "$plugin_root" -maxdepth 2 -name 'plugin.manifest.sig' -print -quit | grep -q .; then
  echo "tasks source/check tree unexpectedly contains a signature" >&2
  exit 1
fi

echo "tasks source and reproducible unsigned WASI build checks passed"
