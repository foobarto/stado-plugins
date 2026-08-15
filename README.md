# stado-plugins

Signed, installable [stado](https://github.com/foobarto/stado) plugins, plus
the **trust anchor** for everything published under `github.com/foobarto/*`.

## Trust anchor

`.stado/author.pub` holds the single Ed25519 public key that signs every
plugin here (EP-39 §C — one key per owner). On first install of any
`github.com/foobarto/...` plugin, stado fetches this key and prompts you once
to trust it (TOFU); subsequent installs from this owner are silent.

```
fingerprint: 57a3e58ce484c5e5
pubkey:      49bf2aa1ae268e2cab7f8e328202244262e62aba8ac4b2653f22f7683118a18e
```

Signing is **offline**: the private key never lives in this repo or its CI.

## Install

```bash
stado plugin install github.com/foobarto/stado-plugins/<plugin>@v0.1.0
```

stado resolves artefacts from `<plugin>/dist/` at the tagged tree
(`plugin.wasm` + `plugin.manifest.json` + `plugin.manifest.sig`), verifies the
signature against the anchor, checks the wasm digest, and installs.

## Plugins

| Plugin | What it does |
|--------|--------------|
| `browser` | Headless HTTP "browser": fetch, parse HTML, follow links/forms, cookie jar (tier 1 + Chrome CDP). |
| `browser-minimal` | Minimal HTML-fetch/parse browser (goquery, no CDP). |
| `encode-zig` | Encoding/decoding helpers (base64/hex/url/…), compiled from Zig. |
| `http-session` | Persistent HTTP session with a cookie jar across calls. |
| `image-info` | Image metadata (dimensions, format, EXIF-lite) without decoding pixels. |
| `guidance` | Explicit opt-in TUI lifecycle application for bounded pre-LLM guidance; unsigned source staged for stado 0.80.0 with no native fallback. |
| `llm-invoke` | Explicit opt-in model-facing completion tool built on stado's authenticated, token-bounded provider primitive; unsigned source staged for stado 0.80.0. |
| `mcp-client` | Talk to an external MCP server from inside stado. |
| `persistent-shell` | A shell whose working dir + env persist across tool calls. |
| `research` | Ordinary isolated memory/session research tools plus package-bound child-only evidence helpers; unsigned source staged for stado 0.80.0. |
| `skills` | Explicit opt-in model skill search/open over digest-fenced context facts; unsigned source staged for stado 0.80.0. |
| `supervise` | Official lifecycle supervision quality-gate application for stado 0.80.0 and newer; current signed package `supervise/v0.1.1`. |
| `tasks` | Explicit opt-in TUI lifecycle application for global broker-artifact tasks, logical tombstones, and fail-closed one-way JSON import; unsigned source staged for stado 0.80.0. |
| `tool-registry` | Explicit opt-in discovery and atomic session tool-surface policy over authenticated registry facts; unsigned source staged for stado 0.80.0. |
| `web-search` | Web search tool. |
| `webfetch-cached` | Fetch a URL with on-disk response caching. |
| `hello` | Minimal example plugin (Rust/zig template). |
| `hello-go` | Minimal example plugin (Go wasip1 template). |
| `state-dir-info` | Reports the plugin's state-dir wiring; example of `state:` caps. |

Each released plugin's capabilities and tools are declared in its
`<plugin>/dist/plugin.manifest.json` — review them before installing. The
supervise source contract remains visible in
`supervise/plugin.manifest.template.json`; its committed `dist/` is the exact
offline-key-signed release bundle. Other staged packages expose their proposed
contracts in source-adjacent manifest templates; unsigned generated bundles
are not release artifacts.

## Versioning

Tag `v0.1.0` is the first cut of the whole set. Future releases may move to
per-plugin subdir tags (EP-39 §A) so plugins version independently.

## Provenance

These were migrated out of the main `stado` repo (EP-0042) to keep compiled
binaries out of the source tree. The wasm here is the same proven build that
shipped in `stado` ≤ v0.52.x; future versions rebuild from source in this
repo's CI (binaries built in CI, manifests signed offline).

## License

Licensed under either of

- Apache License, Version 2.0
  ([LICENSE-APACHE](LICENSE-APACHE) or
  <http://www.apache.org/licenses/LICENSE-2.0>)
- MIT license
  ([LICENSE-MIT](LICENSE-MIT) or
  <http://opensource.org/licenses/MIT>)

at your option.

`SPDX-License-Identifier: MIT OR Apache-2.0`

### Contribution

Unless you explicitly state otherwise, any contribution intentionally
submitted for inclusion in the work by you, as defined in the Apache-2.0
license, shall be dual licensed as above, without any additional terms
or conditions.

Run `./check-obsolete-plugin-abi.sh` before submitting plugin changes. It
checks source, documentation, manifest templates, generated manifests, and
published WASM binaries for retired plugin ABI and capability forms.
`supervise/check.sh` additionally runs that application's unit, race, vet,
reproducible WASI-build, evaluator-CLI, and scenario checks without signing or
leaving a development bundle in the repository. `llm-invoke/check.sh` likewise
checks its strict provider-facts policy and compares two reproducible unsigned
WASI builds. `skills/check.sh` checks strict discovery/load policy, ordinary
tool-result provenance, and two reproducible unsigned WASI builds.
`tasks/check.sh` checks strict task/migration policy, race/vet, and two
reproducible unsigned WASI builds without signing. Each staged package has its
own corresponding checks. Installing any application remains an explicit
operator choice.
