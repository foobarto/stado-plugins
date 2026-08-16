#!/usr/bin/env bash
# Reproducibly build every released plugin without signing it.
# Signing stays an offline operation; CI only needs the public trust anchor.

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

go_plugins=(
  browser
  browser-minimal
  hello-go
  http-session
  image-info
  mcp-client
  persistent-shell
  state-dir-info
  supervise
  web-search
  webfetch-cached
)
zig_plugins=(
  encode-zig
  hello
)
released_plugins=("${go_plugins[@]}" "${zig_plugins[@]}")

if [[ "${1:-}" == "--list" ]]; then
  printf '%s\n' "${released_plugins[@]}" | LC_ALL=C sort
  exit 0
fi

if [[ $# -ne 1 ]]; then
  echo "usage: $0 OUTPUT-DIRECTORY" >&2
  exit 2
fi

required_go_version="go1.26.6"
selected_go_root="$(cd -- "$repo_root" && go env GOROOT)"
selected_go="$selected_go_root/bin/go"
actual_go_version="$(GOTOOLCHAIN=local "$selected_go" env GOVERSION)"
if [[ "$actual_go_version" != "$required_go_version" ]]; then
  echo "released plugin builds require $required_go_version; found $actual_go_version" >&2
  exit 1
fi

required_zig_version="0.15.2"
actual_zig_version="$(zig version)"
if [[ "$actual_zig_version" != "$required_zig_version" ]]; then
  echo "released plugin builds require Zig $required_zig_version; found $actual_zig_version" >&2
  exit 1
fi

output_root="$1"
mkdir -p -- "$output_root"
if [[ -n "$(find "$output_root" -mindepth 1 -print -quit)" ]]; then
  echo "output directory must be empty: $output_root" >&2
  exit 1
fi
output_root="$(cd -- "$output_root" && pwd)"

go_cache="${GOCACHE:-/tmp/stado-plugins-go-cache}"
zig_global_cache="${ZIG_GLOBAL_CACHE_DIR:-/tmp/stado-plugins-zig-global-cache}"
zig_local_cache="${ZIG_LOCAL_CACHE_DIR:-/tmp/stado-plugins-zig-local-cache}"

write_manifest() {
  local plugin="$1"
  local destination="$2"
  local digest
  digest="$(sha256sum "$destination/plugin.wasm" | awk '{print $1}')"
  sed "s/\"wasm_sha256\": \"\"/\"wasm_sha256\": \"${digest}\"/" \
    "$repo_root/$plugin/plugin.manifest.template.json" > "$destination/plugin.manifest.json"
  if ! grep -Fq "\"wasm_sha256\": \"${digest}\"" "$destination/plugin.manifest.json"; then
    echo "$plugin: failed to populate wasm_sha256" >&2
    exit 1
  fi
}

for plugin in "${go_plugins[@]}"; do
  destination="$output_root/$plugin"
  mkdir -p -- "$destination"
  (
    cd -- "$repo_root/$plugin"
    GOCACHE="$go_cache" GOTOOLCHAIN=local GOOS=wasip1 GOARCH=wasm \
      "$selected_go" build -trimpath -buildvcs=false -buildmode=c-shared \
        -ldflags=-buildid= -o "$destination/plugin.wasm" .
  )
  write_manifest "$plugin" "$destination"
done

destination="$output_root/encode-zig"
mkdir -p -- "$destination"
(
  cd -- "$repo_root/encode-zig"
  ZIG_GLOBAL_CACHE_DIR="$zig_global_cache" ZIG_LOCAL_CACHE_DIR="$zig_local_cache" \
    zig build-exe src/encode.zig \
    -target wasm32-freestanding \
    -fno-entry \
    -OReleaseSmall \
    --export=stado_alloc \
    --export=stado_free \
    --export=stado_tool_encode \
    -femit-bin="$destination/plugin.wasm"
)
write_manifest "encode-zig" "$destination"

destination="$output_root/hello"
mkdir -p -- "$destination"
(
  cd -- "$repo_root/hello"
  ZIG_GLOBAL_CACHE_DIR="$zig_global_cache" ZIG_LOCAL_CACHE_DIR="$zig_local_cache" \
    zig build-exe src/hello.zig \
    -target wasm32-freestanding \
    -fno-entry \
    -OReleaseSmall \
    --export=stado_alloc \
    --export=stado_free \
    --export=stado_tool_greet \
    -femit-bin="$destination/plugin.wasm"
)
write_manifest "hello" "$destination"

echo "reproducibly built ${#released_plugins[@]} unsigned released plugin bundles in $output_root"
