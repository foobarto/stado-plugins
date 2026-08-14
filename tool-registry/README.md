# tool-registry

Official signed-WASM policy for model-driven tool discovery and session-scoped
surface changes. Native stado supplies only the bounded authenticated catalog
facts and digest-fenced atomic surface-edit primitives.

The application caps a complete catalog at 4,096 tools and 16 MiB of
serialized facts. It pages one entry at a time so a single valid maximum-size
schema still fits the host's 1 MiB page ceiling; oversized result policy fails
with an actionable error instead of returning partial JSON.
Atomic surface edits carry at most 4,096 exact names in one strict 1 MiB
request; the host validates the complete digest-fenced batch before changing
any name.

The package exposes `tools__search`, `tools__describe`, `tools__categories`,
`tools__in_category`, `tools__activate`, `tools__deactivate`, `plugin__load`, and
`plugin__unload`. Search, formatting, category grouping, describe-and-activate,
and source-group workflow policy all live here rather than in stado core.

This plugin is an explicit operator opt-in. Installing it does not make its
tools non-disableable or silently autoload them. Add the desired discovery
tools to `[tools].autoload` after installing and enabling the exact package.
`plugin__load` and `plugin__unload` accept the exact `source_namespace` returned
by the catalog tools, never a display-name alias. This namespace is stable and
unversioned (for example `stado.dev/bundled/fs`); a versioned canonical package
identity is deliberately not a grouping or mutation selector.

`./build.sh` creates an unsigned development artifact. `./check.sh` runs unit,
race, vet, and two-build reproducibility checks. Release `v0.1.0` must be signed
with the official offline key and must not reuse a development artifact or key.
