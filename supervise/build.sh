#!/usr/bin/env bash
# Reproducible unsigned build. Release signing remains a separate offline step.

set -euo pipefail

required_go_version="go1.26.6"
actual_go_version="$(go env GOVERSION)"
if [[ "$actual_go_version" != "$required_go_version" ]]; then
  echo "supervise release builds require $required_go_version; found $actual_go_version" >&2
  exit 1
fi

output_dir="${SUPERVISE_BUILD_DIR:-dist}"
mkdir -p "$output_dir"
if [[ -e "$output_dir/plugin.manifest.sig" ]]; then
  echo "refusing to rebuild beside an existing signature; remove or archive the signed release artifact first" >&2
  exit 1
fi

GOCACHE="${GOCACHE:-/tmp/stado-plugins-go-cache}" \
  GOOS=wasip1 GOARCH=wasm \
  go build -trimpath -buildvcs=false -buildmode=c-shared \
    -ldflags=-buildid= -o "$output_dir/plugin.wasm" .

digest="$(sha256sum "$output_dir/plugin.wasm" | awk '{print $1}')"
sed "s/\"wasm_sha256\": \"\"/\"wasm_sha256\": \"${digest}\"/" \
  plugin.manifest.template.json > "$output_dir/plugin.manifest.json"

echo "unsigned wasm: $output_dir/plugin.wasm"
echo "unsigned manifest: $output_dir/plugin.manifest.json"
echo "unsigned by design; no plugin.manifest.sig was produced"
