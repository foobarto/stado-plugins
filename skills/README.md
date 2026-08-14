# skills

Official model-invocable skill discovery for stado, implemented as an
explicitly installed WASM plugin.

`skills__search` searches bounded, host-observed/model-admitted facts. `skills__load`
opens the exact opaque ID returned by search under a fresh digest-fenced
catalog and returns its Markdown body as an
ordinary tool result with scope and provenance labels. It never asks native
stado to append a system-prompt listing or synthesize a user message.

The signed package ceiling contains catalog, open, registry, and session
surface capabilities. Per-tool attenuation gives `skills__search` only the
skill-catalog operation; only `skills__load` receives open and activation
authority.

Display names are searchable labels, not identity. Two admitted skills may
share a name; the model loads either one by its exact opaque ID. Fabricated or
stale IDs fail against the current catalog.

The host, not the plugin, omits `disable-model-invocation` skills, keeps
project `allowed-tools` inert, and intersects persona tool declarations with
the exact session ceiling. It also omits rendered bodies above the model
resource ceiling of 128 KiB; explicit operator skill invocation retains its
separate native loader allowance. The plugin may then atomically activate those
effective names through the generic registry/session-surface ABI.

This development source is intentionally unsigned and is not bundled or
default-enabled. A release must be built and signed with the official offline
key before publication.
