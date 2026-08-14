# llm-invoke

`llm-invoke` is the official, explicit opt-in WASM implementation of the
`llm.invoke` MCP tool. It is not bundled, enabled, signed, or published by the
development source in this directory.

The application-facing contract lives here: the tool schema, request shaping,
sampling fields, error presentation, and decision to keep a completed response
when provider cleanup later fails. Native stado exposes only the generic
`stado_provider_invoke` primitive. It constructs the operator-configured
provider without exposing credentials, injects authenticated plugin identity,
enforces the signed token ceiling, propagates cancellation, and returns bounded
provider facts. Neither the plugin input nor the host import accepts a provider
credential, actor, session identity, usage claim, or budget override.
The guest also rejects an impossible host buffer length before slicing and
rejects facts whose reported total exceeds its signed 16,384-token capability.

The former MCP-only persona option is intentionally absent. Persona resolution
and prompt composition are application policy; reintroducing them belongs in a
plugin design rather than the native provider bridge.

## Development validation

```bash
./check.sh
```

The check runs unit, race, and vet validation, then compares two WASI builds
byte for byte. `build.sh` writes an unsigned development bundle to `dist/` (or
`LLM_INVOKE_BUILD_DIR`) and refuses to coexist with a signature. Release
signing uses the repository's offline official key in a separate release step.
