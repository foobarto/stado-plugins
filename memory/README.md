# Memory lifecycle application

This is source-complete, unsigned development material for the plugin-owned
memory/learning cutover. It is not a published or trusted installation.

The application owns `/memory`, `/learn`, the `memory` and `learn` model tools,
and the `memory`/`lesson` EP-63 artifact schemas. Active, non-secret artifacts
may contribute bounded context at `pre_llm`; `/memory on|off` is stored in the
broker application journal for the exact session/generation.

`/learn` and the `learn` tool inspect at most eight exact current-session
evidence items, invoke the configured provider under a signed 12,000-token
ceiling, strictly validate its structured suggestions, and propose candidate
lessons. Each proposal submits the opaque receipt IDs returned by the evidence
broker; the artifact broker re-resolves them for the exact plugin, session, and
generation and derives the persisted provenance refs. They cannot approve or
activate a candidate. Fresh promotion remains
blocked until stado exposes a separately trusted EP-59 presenter that consumes
an operator-origin grant; UI/command origin is intentionally not treated as
that authority.

Provider calls do not yet have a durable idempotency primitive. The application
journals an exact review intent before invocation and journals the bounded,
strictly decoded result before proposing artifacts. A rebind can recover a
durable result without another provider call. The narrow crash window after a
provider succeeds but before its result is journalled is deliberately treated
as ambiguous: an identical retry fails instead of risking a second charge.

The sole historical bridge is the fixed
`stado_artifact_migrate_legacy_memory_v1` import. The guest supplies `{}` only:
the broker owns source/archive paths, exact installed identity and schema
digests, staging, replay fencing, and the atomic completion marker.

Build an unsigned development artifact with `./build.sh`. The script refuses
to build beside a signature and never creates `plugin.manifest.sig`.
