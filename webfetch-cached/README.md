# webfetch-cached — bundled-tool wrapping example

Wraps the `stado_http_request` host import with a SHA-256-keyed
disk cache. It can replace the bundled `webfetch` tool when callers want
the raw response body plus explicit cache metadata.

## What this example demonstrates

Three plugin-surface features in one small plugin:

1. **Wrapping the generic HTTP host import.** Calls
   `stado_http_request` from inside the wasm sandbox and declares the
   corresponding `net:http_request` capability. The plugin rejects
   non-2xx responses and truncated bodies so it never caches a partial
   or unsuccessful response.

2. **Workdir-rooted fs capabilities.** Declares `fs:read:.cache/stado-webfetch`
   and `fs:write:.cache/stado-webfetch` (relative paths, resolved
   against the operator's `--workdir` or the agent loop's session
   worktree). EP-0027.

3. **`[tools].overrides` for transparent replacement.** Add the
   following to `config.toml` and the bundled `webfetch` tool the
   LLM sees IS this plugin — no agent-prompt change, no hand-off:

   ```toml
   [tools]
   overrides = { webfetch = "webfetch-cached-0.1.0" }
   ```

## Build + install

```sh
stado plugin gen-key webfetch-cached-demo.seed       # one-time
./build.sh                                            # compile + sign
stado plugin trust <pubkey-hex> "stado example"       # one-time per signer
stado plugin install .
mkdir -p $PWD/.cache/stado-webfetch                   # cache dir must exist
```

The pubkey hex is printed by `gen-key`; you can re-derive it from
the `author.pubkey` file build.sh writes alongside the seed.

## Run from CLI

```sh
# First call — cache miss, real HTTPS fetch:
stado tool run --workdir=$PWD webfetch '{"url":"https://example.com"}'

# Second call — cache hit, instant:
stado tool run --workdir=$PWD webfetch '{"url":"https://example.com"}'
```

Output is JSON with `cache_hit: true|false` and the response body. The cache
file lands at `$PWD/.cache/stado-webfetch/<sha256>.json`.

If you forget either flag, `stado plugin doctor webfetch-cached-0.1.0`
will tell you which one is missing.

## Run inside the agent loop

With the `[tools].overrides` config above set, the bundled `webfetch`
inside the TUI / `stado run` resolves to this plugin automatically.
The LLM doesn't see a different tool name — it just gets cached
results back.

## Cache invalidation

```sh
rm $PWD/.cache/stado-webfetch/<sha256-prefix>*.json   # one URL
rm -rf $PWD/.cache/stado-webfetch/                    # everything
```

There's no TTL — that's deliberate. URLs that should be re-fetched
periodically belong in a different tool.

## See also

- [`docs/features/plugin-authoring.md`](../../../docs/features/plugin-authoring.md)
  — first-time-author walkthrough
- [`docs/eps/0027-repo-root-discovery.md`](../../../docs/eps/0027-repo-root-discovery.md)
  — why workdir-rooted fs capabilities need `--workdir`
- [`plugins/demos/hello-go/`](../../demos/hello-go/) — minimal Go plugin
  template, no host imports
- [`plugins/demos/approval-bash-go/`](../../demos/approval-bash-go/) —
  alternative bundled-tool wrapping pattern (uses `ui:approval`
  before delegating)
