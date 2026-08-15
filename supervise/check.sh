#!/usr/bin/env bash
set -euo pipefail

plugin_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
first_build="$(mktemp -d)"
second_build="$(mktemp -d)"
eval_binary="$(mktemp)"
cleanup() {
  rm -rf -- "$first_build" "$second_build"
  rm -f -- "$eval_binary"
}
trap cleanup EXIT

cd "$plugin_root"
required_go_version="go1.26.6"
actual_go_version="$(go env GOVERSION)"
if [[ "$actual_go_version" != "$required_go_version" ]]; then
  echo "supervise checks require $required_go_version; found $actual_go_version" >&2
  exit 1
fi
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go test -race ./...
GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go vet ./...

SUPERVISE_BUILD_DIR="$first_build" ./build.sh
SUPERVISE_BUILD_DIR="$second_build" ./build.sh
cmp "$first_build/plugin.wasm" "$second_build/plugin.wasm"
cmp "$first_build/plugin.manifest.json" "$second_build/plugin.manifest.json"
cmp "$first_build/plugin.wasm" "$plugin_root/dist/plugin.wasm"
go_root="$(go env GOROOT)"
if LC_ALL=C grep -aFq "$go_root/" "$first_build/plugin.wasm"; then
  echo "reproducible build leaked the absolute GOROOT: $go_root" >&2
  exit 1
fi

GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" go build -o "$eval_binary" ./eval/cmd/supervise-eval
for scenario in evals/scenarios/*.json; do
  "$eval_binary" scenario "$scenario" >/dev/null
done
"$eval_binary" score --input evals/observation-example.jsonl >/dev/null

echo "supervise source, WASI build, and evaluation checks passed"
