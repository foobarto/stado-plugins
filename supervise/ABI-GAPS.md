# Supervise ABI integration ledger

This is an implementation ledger, not a request for supervise-specific native
workflow code. It distinguishes generic capabilities a sandboxed lifecycle
application cannot create for itself from policy and cross-repository work that
must remain in the official plugin.

Current stado already provides persistent serialized WASM applications, signed
manifest tools and commands, opaque application bindings, durable event
delivery/cursors, generic `session.turn_committed` facts, journal/projection,
broker holds and pause/stop, timer promotion, payload-derived idempotency, and
provider/tool scheduling gates including the strict-live wait loop. Those are
closed foundations, not gaps.

Leased holds are now renewed by the plugin halfway through their configured TTL
for watchdog, verifier, and stale-intervention confirmation work. Exact hold ID
and CAS version live in the journal; version-derived request idempotency makes a
post-crash replay return the same renewal. A dedicated durable half-TTL timer is
scheduled on acquisition and every renewal, independently of reviewer polling.
Renewal/timer failure requests a pause without relying on the possibly expired
hold. A completed application continues renewing until the successful-
completion handoff and exact release both succeed.

Successful completion is also closed: the signed application capability
`session:complete` admits `stado_session_complete`, which records ordinary
successful termination separately from pause/stop. The plugin invokes it only
after a fresh verifier's exact current-anchor approval, persists the handoff,
and CAS-releases the exact hold version. Broker idempotency covers a crash at
either boundary.

Completion-time native verification is closed through the generic
`session:verification:request` capability and targeted
`session.verification_finished` event. During the exact still-pending committed
turn, the plugin holds the worker and requests the operator-configured suite by
run ID, active worker version, source event sequence, and stable idempotency key.
The broker derives the Git tree/turn anchor and refuses claim until that event's
cursor ACK is durable. The terminal guest payload is the strict
`stado.dev/session-verification-facts/v1` result-only shape: source and source
evidence, suite/command/result digests, typed factual outcomes/failures, and
broker WAL evidence. It exposes no command text/output, application authority,
routing identity, or completion decision.

The plugin journals request intent before the call and terminal facts before
hold release, correction, or pause. Exact callback replay resumes an unfinished
effect; stale or operator-obsoleted facts are discarded. `no_suite` is recorded
as absence, never success: contract verification prose is not executable, and
the fresh independent verifier is told explicitly that no operator suite ran.
Native execution remains at-least-once only in the irreducible
command-finished/before-terminal-WAL crash window.

Fresh operator-input classification is closed through the generic C28/C50
contracts. The plugin journals a stable review intent, CAS-claims the immutable
queued record as `reviewing`, and only then acknowledges the mandatory event.
It spawns a fresh idempotent read-only child at the exact turn anchor, accepts
only its authenticated matching `agent.down`, and routes with the same exact
input/run/version/review ID. There is no lexical classifier. Uncertainty,
invalid output, terminal failure, or exhausted bounded spawn/read recovery
conservatively defers the unchanged original. Reviewing projection, deadlines,
attempts, backoff, child replacement after process restart, route receipts, and
application-selected deferred order are journal-replayable.

The 64-item continuation boundary also fails closed. A further conservative
deferral enters a durable overflow-pause phase; the plugin requests typed pause
and leaves the exact claimed original for broker terminal recovery. It neither
drops the bytes nor reclassifies unrelated work to evade the bound.

Queued input also invalidates any in-flight completion candidate/conclusion in
the same durable intent append. Obsolete completion children are cancelled and
the exact quality hold is released only after invalidation; a fresh explicit
current-anchor completion decision is then required. Terminal worker recovery
settles unresolved originals without converting them into deferred tasks.

Exact reviewer profiles are closed through generic C48. The manifest declares
both `agent:spawn` and `agent:spawn:configure`; baseline, watchdog, and verifier
requests carry the setup-selected provider/model, thinking, thinking-token
budget, and reasoning effort byte-for-byte. Native stado retains credentials,
validates model capabilities, and fails closed rather than silently
substituting. The configure capability alone grants no spawn operation or
credential access.

Exact selected workflow configuration is also closed: artifact query accepts
immutable `refs:[{id,version}]`, so restart/tool revalidation cannot lose the
journalled candidate behind a bounded recency page. The signed
`artifact:read:self#supervision-contract` capability and exact guest
`self#supervision-contract` query are resolved from authenticated native
identity, so official source does not hardcode its release namespace.

Pivot selection and dormant lifecycle behavior are plugin-owned and closed.
The model proposes a strict complete replacement baseline; the application
computes plan-only versus contract-changing policy, keeps a quality hold across
review/confirmation/edit, and uses generic `artifact:edit:supervision-contract`
to create the exact next candidate version. Watchdog autonomy is limited to
configured plan-only changes. Everything else requires `/supervise resume` and
`stado_ui_approve`, which grants no security authority. Intent, expected version,
selection, plan re-anchor, evidence invalidation, and exact release are durable
crash boundaries. With no setup, or after exact cancellation/completion cleanup,
the persistent application ACKs/no-ops and does not deny ordinary work.
Cancellation retains the exact baseline/reviewer child as a durable cleanup
obligation; a failed cancel call cannot be converted into dormancy, and a new
setup cannot replace the state until that cancellation, terminal worker CAS,
and exact hold release are journalled. A fully cleaned terminal run is
self-contained on reload and no longer queries a historical artifact merely to
ACK unrelated work.

The host and plugin also keep byte-identical
`testdata/session-turn-facts-v1.json` fixtures with strict decoder tests.
[TURN-FACTS.md](TURN-FACTS.md) is the exact consumer contract. Provider
input/output are per-turn counters; the plugin owns the durable cumulative
worker-token ledger. The fact shape deliberately has no completion candidate;
only the plugin's explicit durable completion tool may create one.

Crash-safe generic agent admission is closed. The plugin supplies a stable
bounded `idempotency_key` and unique quality-only ownership label for every
baseline/reviewer/verifier spawn. Fleet namespaces the key by authenticated
application/session/generation, binds it to the normalized request digest,
serializes concurrent admission, returns the exact original child on replay,
and conflicts on changed input. The plugin journals the logical intent before
spawn; if the callback loses both reply and child ID, authenticated terminal
facts can bind only the uniquely labelled pending intent. Module rebind replays
the same child. After a process restart, when the old process-local Fleet child
is necessarily gone, the same key admits and journals one replacement without
consuming a new policy attempt.

Every boundary after a child ID is returned is restart-safe in this tree. The
plugin strictly validates the exact journal acknowledgement and forces an
authoritative projection refold after any ambiguous append result. Setup cancel,
baseline rejection/terminal/ready, artifact selection, worker request, and run
cancel transitions therefore recover according to whether the broker append
committed, never according to a stale in-memory mutation. Run IDs use fresh
WASI cryptographic randomness; callback-local sequence values are deliberately
not durable identity.

No generic primitive blocks the bounded C36 setup/command state machine. The
supported-surface restriction and remaining EP-62/64 work below are separate
integration boundaries.

## Supported-surface boundary

1. TUI is the only supported lifecycle-application surface for the current
   release. `stado run`, headless JSON-RPC, and ACP reject configured lifecycle
   applications before provider/session work rather than assembling a partial
   second instance. Supporting another surface requires owning the same single
   persistent command/tool/hook/event instance and every generic broker bridge.

## Recently closed generic primitives

Application-owned setup and recurrence are now closed. The signed command has
its own bounded 15-minute timeout while lifecycle callbacks retain their short
deadline. The plugin sequences normal `stado_ui_choose` fields, journals setup,
spawns a fresh read-only baseline architect, strictly validates and renders the
full proposal, and asks for quality confirmation before proposing an artifact.
It then calls `stado_session_worker_request` and returns the exact
`worker_run_id`; only the native command host can activate it. Durable
`status|resume|cancel` reconcile the generic worker projection, CAS-cancel exact
requested/resume-requested/active versions, and never resurrect a cancelled,
completed, or stopped recurrence. An interrupted recurrence uses the generic
`session:worker:resume` CAS to enter `resume_requested`; the command's distinct
`resume_worker_run_id` asks the native controller to reactivate the exact same
run. Active holds keep that request pending and retryable rather than silently
resuming. UI origin is not treated as security authentication and no
supervise-specific host import or native fallback was added.

The complete EP-62 reviewer retry policy is plugin-owned and implemented:
event reviews receive three fresh attempts, ten consecutive exhausted triggers
pause standard supervision, live review retries with bounded exponential
backoff, and three unsuccessful correction follow-ups pause. Every attempt,
due time, counter, handoff, and correction state is journal-replayable. A valid
verdict resets the event-failure streak and survives later cleanup diagnostics.

The following accepted EP-62/64 product behavior remains integration work, not
permission to weaken the contract:

1. a cross-repository integration proving that the landed automatic
   compacted-child handoff preserves this existing application scope;

The generic handoff is implemented and the plugin accepts only the exact
authenticated parent/session scope; its full stado-plus-official-plugin
integration still needs release proof.

Reviewer and verifier admission now sends
`source:{at:<authenticated turn_ref>}`. The host derives and
ancestry-authorizes the logical session, verifies the immutable commit, and
pins it synchronously before asynchronous admission. The plugin accepts no
`turns/N`, `last_committed_turn`, or current-tip fallback; delayed/replayed
reviews remain on the event anchor and begin with a fresh conversation.

Terminal child reads now carry host-collected input/output/cache token counters
with an explicit completeness bit plus a bounded cleanup kind/fingerprint. The
durable `agent.down` event carries the same facts together with generic child,
admitted-budget, scope, change, failure-fingerprint, and immutable-evidence
facts. The plugin strictly decodes that versioned payload, binds it to the exact
pending child and authenticated parent envelope, and derives reviewer/verifier
policy in WASM. It journals watchdog and verifier usage separately and exactly
once from the event. Agent polling supplies semantic output; a poll waits for
the durable terminal observation and its terminal metadata must match. Usage
incompleteness and cleanup remain diagnostics, and a later cleanup failure
cannot erase valid semantic output. No USD field participates in policy. See
[AGENT-DOWN-FACTS.md](AGENT-DOWN-FACTS.md).
## Plugin-owned surface now implemented

The signed manifest declares three canonical per-tool exports documented in
[MODEL-TOOLS.md](MODEL-TOOLS.md). They accept only the exact session-selected
contract candidate and exact durable plan anchor, keep a bounded idempotency
ledger, append application journal state, and derive review signals. No native
supervise policy or second tool dispatcher is required.
All three explicitly opt into the generic application-worker projection with
`plan_visible:true`; the host, rather than the plugin, binds them to the exact
active worker and continues enforcing Do/Plan/BTW and global tool ceilings.
