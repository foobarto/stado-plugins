# persistent-shell

Wraps stado's host-side PTY registry as seven plugin tools so an agent
can drive interactive shells across multiple tool calls.

> Status: example plugin. Requires the `exec:pty` capability (added in
> stado v0.27).

## Why

Every other tool a wasm plugin can drive is stateless request→response.
Real shell work — driving an `ssh` session, watching a `nc` listener,
running `msfconsole` step-by-step, attaching `tmux` from a TUI agent
— needs interactive stdin/stdout *and* needs the process to keep
running between tool calls.

Putting the PTY registry **host-side** (one per stado runtime) and
identifying sessions by uint64 id gives:

- Persistence across plugin instance freshness — a session created in
  one tool call is reachable from any later call without the wasm
  module having to keep state.
- Per-session ring-buffered output (default 64 KiB, terminal
  scrollback semantics) so a session can run while no one is reading,
  then let a later reader catch up.
- Natural parallelism — every `shell.create` returns a fresh id;
  subagents can spawn independent sessions without coordination.
- Handle-based access across calls without a separate attach lock;
  signal, resize, and destroy remain out-of-band operations.

## Tools

| Tool | Args | Returns |
|---|---|---|
| `shell_create` | `argv`/`cmd`, `env`, `cwd`, `cols`, `rows`, `buffer_bytes` | `{id}` |
| `shell_list` | — | `[{id, cmd, alive, started_at, buffered, dropped, exit_code?}]` |
| `shell_write` | `id`, `data` (UTF-8) **or** `data_b64` (raw) | `{n}` |
| `shell_read` | `id`, `max_bytes`, `timeout_ms` | `{data?, data_b64, n, eof?}` |
| `shell_signal` | `id`, `sig` (POSIX) | `{ok: true}` |
| `shell_resize` | `id`, `cols`, `rows` | `{ok: true}` |
| `shell_destroy` | `id` | `{ok: true}` |

`shell_read` returns `data` (the bytes as a UTF-8 string) when the
content looks like plain text and `data_b64` (always populated) for
the wire-safe form. Use `data_b64` when you need byte-exact handling
(escape sequences, hex dumps); use `data` otherwise.

## Workflow patterns

### Drive a long-lived bash

```jsonc
// Create.
shell_create({"argv": ["/bin/bash"], "cols": 120, "rows": 40})
  // → {"id": 1}

// Send commands.
shell_write({"id": 1, "data": "id\n"})
shell_read({"id": 1, "timeout_ms": 500})
  // → {"data": "$ id\nuid=1000(...) ...\n", ...}

// Send Ctrl-C as raw bytes.
shell_write({"id": 1, "data_b64": "Aw=="})  // 0x03

// Tear down.
shell_destroy({"id": 1})
```

### Catch a reverse shell while doing other work

```jsonc
// Listener output buffers in the ring.
shell_create({"cmd": "nc -lvnp 9001"})
  // → {"id": 2}
// ... do other work ...

// Later: read everything that arrived.
shell_read({"id": 2, "timeout_ms": 0})
  // → {"data": "connect to ...\n$ ", ...}
shell_write({"id": 2, "data": "id\n"})
```

### Hand a session off between subagents

Pass the PTY id to the subagent. Reads consume the shared byte stream, so
coordinate which agent is reading even though the ABI no longer has a
separate attach lock.

```jsonc
// parent creates and configures id=3, then passes it to a subagent
// subagent writes/reads id=3, then returns ownership by convention
// parent resumes reading
shell_read({"id": 3, "timeout_ms": 0})  // sees what subagent did
```

## Capability

Manifest must declare:

```json
"capabilities": ["exec:pty"]
```

This is a coarse capability (any command). Per-command-pattern
capabilities aren't supported in v0.1; an operator who wants to
restrict the executable surface can run the plugin only against
trusted manifests.

## Build

```bash
stado plugin gen-key persistent-shell-demo.seed   # one-time
./build.sh
```

Produces `plugin.wasm` + signed `plugin.manifest.json`. Drop into your
operator-side plugins dir and `stado plugin install ./plugin.manifest.json`.

## Limitations

- Multiple concurrent readers on one PTY are not useful: bytes are consumed
  on read. Coordinate handle ownership at the workflow level.
- Ring buffer is byte-counted, not line-counted. Default 64 KiB —
  enough for ~800 lines of 80-col text. Override per session via
  `buffer_bytes` (4 KiB-4 MiB).
- No cross-runtime PTY sharing. Each stado runtime has its own
  registry; sessions don't migrate.
- The session's child process inherits whatever environment + cwd the
  stado process has unless the caller passes `env` / `cwd`. `env`
  *replaces* the inherited environment; it doesn't extend it.
