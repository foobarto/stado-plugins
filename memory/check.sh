#!/usr/bin/env bash
# Source-only reproducibility check. It builds into a private temporary
# directory and deliberately never signs or publishes the result.

set -euo pipefail

format_diff="$(gofmt -d .)"
if [[ -n "$format_diff" ]]; then
  echo "$format_diff" >&2
  echo "Go sources are not gofmt-clean" >&2
  exit 1
fi

GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test ./...

check_dir="$(mktemp -d)"
trap 'rm -rf -- "$check_dir"' EXIT
MEMORY_BUILD_DIR="$check_dir" ./build.sh

test -s "$check_dir/plugin.wasm"
test -s "$check_dir/plugin.manifest.json"
test ! -e "$check_dir/plugin.manifest.sig"

wasm_digest="$(sha256sum "$check_dir/plugin.wasm" | awk '{print $1}')"
grep -Fq "\"wasm_sha256\": \"$wasm_digest\"" "$check_dir/plugin.manifest.json"

echo "source check passed"
echo "wasm sha256: $wasm_digest"
echo "no signature or persistent dist artifact was produced"
