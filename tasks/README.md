# tasks

Unsigned development source for the explicit `tasks` lifecycle application.
It owns the dynamic `/tasks` command, the session-turn `tasks` model tool, and
the plugin-defined global `task` artifact kind. Nothing is loaded by default.

All ordinary CRUD uses broker artifacts. Delete creates a versioned logical
tombstone (`deleted: true`); there is no plugin JSON store and no dual write.

On first admitted use, the application checks only
`cfg:state_dir/tasks/tasks.json`. A valid legacy array is proposed with stable
broker-namespaced idempotency keys, fully re-queried as immutable ID/version
refs and compared, copied to a digest-named recovery archive, read back, and
only then replaced by a strict receipt after one final source-content check.
The archive preserves the exact original bytes. Restarts and concurrent
application instances converge through the broker keys and receipt-bound refs;
later edits and tombstones do not invalidate the immutable migration proof.
Missing files are ignored; unreadable, malformed, concurrently changed, or
oversize files fail closed. A file larger than the host's exact 16 MiB guest
read ceiling must be copied aside and migrated manually; it is never truncated
or treated as successfully archived.

Live tasks carry a dedicated selector tag, so normal listing is bounded to the
1,000-task operational limit even if versioned tombstones accumulate. Model
`create` requires a caller-chosen `idempotency_key`; retries of the same logical
create must reuse it. `/tasks add` likewise requires `--key`, and the interactive
flow asks for the stable key before mutating. This makes reply-loss and
cross-process retries explicit instead of pretending a callback sequence is a
durable operator operation ID. Reusing a key with different task data fails
closed.

`./check.sh` runs unit/race/vet checks and compares two unsigned WASI builds.
`./build.sh` refuses to build beside a signature and never signs or publishes.
