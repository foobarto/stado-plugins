# research

Official unsigned-development source for Stado's agentic research tools.

`memory__research` and `session__research` are ordinary signed WASM tool
workflows. Each starts a broker-created synchronous child AgentLoop with the
bundled `researcher` persona, read-only mode, a fixed turn/time/token budget,
and an exact three-tool projection for the selected corpus. The package owns
the prompt, search workflow, result shape, and research policy.

The six `research__*` helpers are signed `agent_child_only` tools. They are not
registered on ordinary parent turns and appear only when this package spawns a
child request naming the exact helper in `narrow_tools`; Stado binds that
projection to the loader-verified signed spawning package namespace. Direct native,
bundled-agent, and unrelated-package spawns cannot guess a helper into scope.
Their per-tool capabilities are exact: an artifact helper cannot read session
history and a session helper cannot read artifacts.

Native Stado owns only the generic evidence boundary: authenticated corpus
scope, immutable locators, aggregate read budgets, durable read receipts, and
mechanical checks that a citation excerpt occurs in bytes opened by the exact
direct read-only child. It deliberately makes no claim that the excerpt
semantically entails the model's claim.

Run `./check.sh` for unit, race, vet, and reproducible unsigned WASM checks.
`build.sh` never creates a signature and refuses to overwrite beside one.
