#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$repo_root"

failed=0

report_hits() {
  local heading="$1"
  shift
  local output
  output="$("$@" || true)"
  if [[ -n "$output" ]]; then
    printf '%s\n%s\n' "$heading" "$output" >&2
    failed=1
  fi
}

# Keep the literal compatibility names confined to this checker. Source,
# documentation, templates, generated manifests, and binary imports all count.
report_hits "obsolete plugin ABI/capability references:" \
  rg -n --hidden \
    --glob '!.git/**' \
    --glob '!*.wasm' \
    --glob '!check-obsolete-plugin-abi.sh' \
    '(terminal:open|stado_terminal_|net:http_get|stado_http_get)' .

report_hits "dist WASM binaries containing obsolete plugin ABI/capability references:" \
  rg -a -l \
    --glob '*/dist/*.wasm' \
    '(terminal:open|stado_terminal_|net:http_get|stado_http_get)' .

# A bare net:<host> grant is obsolete. HTTP authority must be expressed as
# net:http_request or net:http_request:<host>. Other current net namespaces
# used by official plugins are explicitly accepted here.
net_failed=0
while IFS= read -r hit; do
  [[ "$hit" =~ \"(net:[^\"]+)\" ]] || continue
  capability="${BASH_REMATCH[1]}"
  case "$capability" in
    net:http_request|net:http_request:*|net:http_request_private|net:dial:*)
      ;;
    *)
      if (( net_failed == 0 )); then
        printf '%s\n' "obsolete or unknown net capability references:" >&2
      fi
      printf '%s\n' "$hit" >&2
      net_failed=1
      failed=1
      ;;
  esac
done < <(rg -n -o --hidden --glob '!.git/**' --glob 'plugin.manifest*.json' '"net:[^"]+"' . || true)

# Ordinary tool authority must be explicit in signed bytes. In particular,
# `[]` means zero authority and must never canonicalize like an omitted field
# that could inherit the package ceiling. Persistent lifecycle applications
# are the opposite: every callback/tool shares one Host, so per-tool fields are
# forbidden rather than advertised as false attenuation.
while IFS= read -r manifest; do
  if ! jq -e '
    .capabilities as $package |
    if (.lifecycle != null) then
      all(.tools[]?; has("capabilities") | not)
    else
      all(.tools[]?;
        has("capabilities") and
        (.capabilities | type == "array") and
        ((.capabilities | length) == (.capabilities | unique | length)) and
        all(.capabilities[]; . as $cap | $package | index($cap) != null))
    end
  ' "$manifest" >/dev/null; then
    printf 'invalid per-tool capability presence/subset: %s\n' "$manifest" >&2
    failed=1
  fi
done < <(find . -name plugin.manifest.template.json -type f -not -path './.git/*' -not -path '*/dist/*' | sort)

if (( failed != 0 )); then
  exit 1
fi

echo "plugin ABI compatibility check passed"
