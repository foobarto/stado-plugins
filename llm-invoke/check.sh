#!/usr/bin/env bash

set -euo pipefail

GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test -race ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go vet ./...

temporary_root="$(mktemp -d /tmp/stado-llm-invoke-check.XXXXXX)"
trap 'rm -rf -- "$temporary_root"' EXIT

LLM_INVOKE_BUILD_DIR="$temporary_root/first" ./build.sh
LLM_INVOKE_BUILD_DIR="$temporary_root/second" ./build.sh
cmp "$temporary_root/first/plugin.wasm" "$temporary_root/second/plugin.wasm"
cmp "$temporary_root/first/plugin.manifest.json" "$temporary_root/second/plugin.manifest.json"
test ! -e "$temporary_root/first/plugin.manifest.sig"
test ! -e "$temporary_root/second/plugin.manifest.sig"

echo "llm-invoke unit, race, vet, and reproducible unsigned build checks passed"
