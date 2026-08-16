#!/usr/bin/env bash
# Public-key-only release integrity and reproducibility gate.

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
first_build="$(mktemp -d)"
second_build="$(mktemp -d)"
cleanup() {
  rm -rf -- "$first_build" "$second_build"
}
trap cleanup EXIT

cd -- "$repo_root"

./check-obsolete-plugin-abi.sh
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" \
  go run ./tools/distcheck/main.go .

mapfile -t inventory_released < <(
  jq -r '.plugins[] | select(.status == "released") | .path' plugin-inventory.json | LC_ALL=C sort
)
mapfile -t builder_released < <(./build-released-plugins.sh --list)
if ! diff -u \
  <(printf '%s\n' "${inventory_released[@]}") \
  <(printf '%s\n' "${builder_released[@]}"); then
  echo "released inventory and reproducible builder disagree" >&2
  exit 1
fi

./build-released-plugins.sh "$first_build"
./build-released-plugins.sh "$second_build"

for plugin in "${inventory_released[@]}"; do
  cmp "$first_build/$plugin/plugin.wasm" "$second_build/$plugin/plugin.wasm"
  cmp "$first_build/$plugin/plugin.manifest.json" "$second_build/$plugin/plugin.manifest.json"
  cmp "$first_build/$plugin/plugin.wasm" "$repo_root/$plugin/dist/plugin.wasm"
done

echo "released plugin signatures, manifests, and reproducible builds passed"
