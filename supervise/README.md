# supervise

Source for the official, signed/installable stado supervision application. It
is an application-sized WASI plugin, not bundled native stado workflow code.

`build.sh` always produces an unsigned reproducible bundle and never invokes
the offline signing key. The signed `dist/` bundle is created only during the
release ceremony and is immutable at the package tag.

The first immutable package is plugin version `0.1.0`, tagged
`supervise/v0.1.0`. Its host compatibility floor is separately
`min_stado_version: 0.80.0`; the plugin package is not versioned as stado
`0.80.0`.

## What lives here

The plugin owns the parts of supervision that can legitimately vary between
applications:

- event-triggered review plus an optional review every N worker turns;
- live review, with an optional strict barrier acquired before review starts;
- deterministic retry, verification, scope, child, milestone, completion, and
  four-turn progress-stall detectors;
- reviewer prompts, strict verdict decoding, coalescing, and workflow state;
- the asymmetric stale-result policy;
- signed quality classification of immutable operator follow-ups as ready or
  deferred, with broker-owned exact delivery and continuation;
- token-only reviewer and verifier budgets.

The four-turn detector measures approved-plan progress. New evidence, a changed
tree, or other activity does not reset it. A completed step or changed active
step does.

Each event-triggered review receives three fresh bounded attempts by default.
Only after all three fail does the trigger count as failed; ten consecutive
exhausted triggers pause the standard profile, while any valid current verdict
resets that streak. Live review instead retries indefinitely with a durable
500 ms exponential delay capped at eight seconds. Attempt number, next due
time, last failure, and both failure counters live in the application journal,
so callback loss or module rebind cannot shorten or restart the policy window.

A correction is followed by a fresh review on the next committed worker turn.
Three correction follow-ups that still require correction pause the workflow.
Each valid watchdog verdict may carry a bounded handoff of open concerns,
hypotheses, interventions, missing evidence, and suggested probes; that handoff
is journalled and supplied to the next fresh watchdog instead of depending on
conversation continuity.

A successfully parsed verdict remains the semantic result even if the provider
later reports cleanup failure. Stale results have three deliberately different
meanings:

- stale `continue`/`approve` is discarded;
- stale `correct` is queued as labelled earlier-anchor advice;
- stale `pause`/`stop` first acquires a durable broker hold and starts a fresh
  review at the current anchor.

Only a current verdict can release that hold, steer, pause, or stop. A current
`stop` becomes `stado_session_request_stop`; the model's prose is never itself a
control transition.

Every acknowledged hold carries its exact broker ID, CAS version, lease expiry,
and next renewal deadline in the application journal. The plugin renews halfway
through the configured TTL while a watchdog, verifier, or stale-intervention
confirmation is unresolved, even when review lasts for the full 3600-second
timeout. Each acquisition/renewal schedules its own durable half-TTL wake, so
lease safety does not depend on reviewer polling continuing. Version-derived
idempotency makes a retry after a crash replay the same renewal instead of
creating a parallel hold. Renewal or renewal-timer failure requests a
hold-independent pause and preserves the unresolved review for recovery; it
never resumes work on the assumption that an expired lease was harmless.

Supervision is a quality gate, not a security boundary. The plugin may be
wrong, steered by repository content, or unavailable. Stado's broker, sandbox,
capability ceiling, authenticated anchors, operator grants, and scheduling
enforcement remain the security boundary.

## Authority boundary

The application consumes host-authenticated facts and requests narrow effects.
It never opens stado's WAL, treats `stado_cfg_state_dir` as shared authority, or
falls back to ambient files. The manifest declares an EP-0063
`supervision-contract` kind and reads it through the authenticated
`self#supervision-contract` selector, so source does not hardcode a future
release identity. `/supervise start` first runs a sequence of normal
bounded choices for objective, cadence, pivot policy, assurance profile,
recurrence conflict, and optional advanced settings. It journals that setup
before spawning a fresh read-only baseline architect. The architect must return
a strict versioned proposal containing constraints, non-goals, acceptance
criteria, an ordered plan with explicit done conditions, definition of done,
verification, and risks. Criterion evidence and ordered-plan progression remain
separate state throughout the workflow.

The application renders the exact proposal and asks for a quality-workflow
confirmation. Rejection creates no artifact or worker. Confirmation proposes a
session-scoped candidate, journals its exact artifact ID/version as private
workflow configuration, and requests one generic application-owned worker
recurrence. The command returns only that broker-issued `worker_run_id`; the
native command host alone may fetch and activate the exact request under EP-64.
Neither the UI response nor the returned ID is a security grant. The artifact
remains a candidate: selection adds no plugin capability and does not make the
content general prompt authority. On restart the application requires that
exact journalled candidate and fails closed if it is missing or changed.
Revalidation uses the generic immutable artifact-query `refs:[{id,version}]`
filter, so newer candidates cannot hide it behind a recency page.

`/supervise status` reports durable setup, plan, review, completion, and worker-
run state. `/supervise resume` retries an interrupted baseline, presents a ready
proposal for confirmation, resolves an exact reviewed pivot, retries an
uncommitted worker request, or CAS-moves
the exact interrupted worker recurrence to `resume_requested`. It returns the
distinct `resume_worker_run_id`; only the native controller may reactivate that
same run, and an active hold keeps it pending for exact retry after release or
expiry. It never revives a broker-terminal `cancelled`, `completed`, or
`stopped` run. `/supervise cancel` CAS-cancels the exact requested,
resume-requested, or active recurrence, cancels a pending review child, clears
application policy work, and releases an exact hold. The cancellation is
journalled before local cleanup. When the application itself committed the
broker `cancelled` transition it returns `cancel_worker_run_id`, and the native
host re-reads that exact terminal projection before stopping the local
provider/tool turn and recurrence. A run already terminalized as
`interrupted|stopped` needs no cancellation handoff and is never mislabelled as
one. After cleanup, a new explicit setup may begin in the same session.

Cancellation does not become dormant merely because the local policy pointer
was cleared. The exact pending reviewer (or setup baseline child) is retained
as a journalled cleanup obligation. A lost spawn reply is recovered before run
cancellation is committed; a failed child-cancel request leaves lifecycle work
fenced and retryable. Only terminal worker CAS, accepted child cancellation,
exact hold release, and the final cleanup journal make the run dormant.

With no selected setup or run, the installed application is dormant: lifecycle
boundaries continue and subscribed events are acknowledged without policy
work. Setup blocks implementation only after it is durably configured and until
its baseline is confirmed or cancelled. A cancelled run becomes dormant only
after its worker is terminal, pending review is cleared, and the exact hold is
released and journalled. A completed run remains fenced through the successful
completion handoff and exact hold release, then becomes dormant as well. The
persistent instance stays available for `status` and a later explicit `start`.
After that terminal cleanup, reload validates the self-contained terminal
journal instead of requiring the old artifact candidate to remain available;
historical storage availability cannot block unrelated future lifecycle work.

Pivot requests carry a complete strict replacement baseline. The plugin, not
the worker or reviewer, classifies it by comparing every non-plan field with the
exact selected contract. A configured watchdog may autonomously approve only a
current-anchor plan-only replacement. User-policy plan changes and every
contract change wait durably for `/supervise resume`, which renders the exact
old/new values and asks for quality confirmation. Approval stages a CAS edit of
the same session-scoped artifact into a new candidate version; it grants no OS
authority, capability, or general prompt authority. Selection increments and
re-anchors the plan version, invalidates completion and verification conclusions,
and preserves evidence only for criteria that remain byte-identical at the same
index. Lost edit replies recover through the deterministic next artifact version.

The setup and command path is designed around callback cancellation and broker
reply loss. A new setup run receives 128 bits from WASI's cryptographic random
source; the lifecycle callback sequence is never used as durable identity
because it restarts when a module is rebound. Every journal reply is decoded
strictly and checked against the exact session, generation, run, transition,
state payload, and evidence list that was submitted. Any missing or mismatched
reply invalidates in-memory state. The next command, event, tick, or tool call
refolds the broker projection before doing more work, so a lost reply recovers
correctly whether the append committed or not. A command timeout may destroy
the module but cannot make its memory authoritative.

Every baseline/reviewer/verifier request carries a stable application-scoped
`idempotency_key` and unique quality-only ownership label. Generic Fleet
admission returns the exact child for same-process/module replay and rejects a
changed request under that key. If a callback loses the spawn reply before the
Fleet control handle is journalled, the durable intent can bind authenticated terminal
facts only from that unique child. After process restart, the previous
process-local Fleet child is gone, so polling the same durable intent admits
and journals one replacement without consuming another policy attempt. No
local marker, guessed child, or supervise-specific host import is used.

Rapid state is journalled after every policy transition and reconstructed from
the broker projection. Reviewers are fresh, token-bounded children admitted as
generic read-only research explorers. They receive only narrow `fs__*`,
`rg__search`, and `readctx__read` tools. Their evidence IDs remain host
references. Every spawn passes the exact authenticated immutable
`git:<tree-ref>@<commit>#turn-N-iteration-M` fact as `source.at`; the host pins
that tree before admitting the asynchronous child and starts a fresh
conversation. Delayed or replayed reviews therefore cannot drift to a newer
worker tip. Mutable selectors and current-tip fallback are rejected; no
supervise-specific read/search/follow import is needed.

The advanced setup can select exact provider/model, thinking mode, thinking-
token budget, and provider-native reasoning effort independently for watchdog
and verifier. The signed manifest grants `agent:spawn:configure`; it grants no
credentials or spawn operation by itself. Stado resolves configured native
credentials, refuses provider/model substitution, and fails closed when a
forced thinking or effort setting is unsupported. The plugin stores only these
bounded quality-profile choices and token caps; no USD budget participates.

The subscribed `session.turn_committed` payload is the generic, strict,
versioned observation contract in [TURN-FACTS.md](TURN-FACTS.md). Native stado
reports authenticated facts; this application derives scope, stall, pivot,
risk, and evidence policy. The host fact shape contains no completion field:
assistant prose and no-tool finality cannot start completion review. Only the
explicit, exact-anchor `supervise__request_completion` tool creates that durable
application candidate.

Terminal children arrive through the separate generic, strict
`stado.dev/agent-down-facts/v1` contract in
[AGENT-DOWN-FACTS.md](AGENT-DOWN-FACTS.md). Parent session and generation come
only from the authenticated event envelope. The plugin binds an observation to
a review only for the exact pending Fleet control handle. The separate child
session ID binds immutable tree and trace evidence. The plugin compares the
remaining generic identity, scope, budget, change, and evidence facts with the signed spawn
request, and derives any policy rejection itself. Host token counters are
accumulated once from this durable event; polling is only the source of the
semantic verdict. A poll that races ahead waits for the event, and bounded
cleanup or incomplete-usage diagnostics cannot erase a valid verdict.

Operator input while an application worker owns recurrence is never parsed as
command authority. The host captures the exact UTF-8 text, digest, owner, run,
and ordinal, then emits the mandatory `operator.input.queued` fact. The plugin
journals a stable fresh-review intent, CAS-claims that exact input as
`reviewing`, and only then acknowledges the event, allowing the reviewer's later
`agent.down` fact to advance through the same mandatory cursor. The reviewer is
a fresh idempotent read-only child pinned to the exact current turn anchor.
There is no lexical fallback: malformed output, uncertainty, terminal failure,
or exhausted bounded spawn/read recovery conservatively routes the immutable
original to `defer`. No route may replace, drop, or retarget its text.

Review claims, child identity, overall deadline, bounded transport attempts,
and backoff due time are journal-replayable. Same-process spawn replay returns
the same child; a full process restart replays the stable logical key and
journals the replacement before reading it. The broker's exact reviewing
projection recovers claim callbacks lost across module rebind. Terminal worker
recovery settles a still-queued/reviewing original without inventing a plugin
classification.

The application continuation set is bounded to 64 deferred originals. If a
fresh reviewer conservatively defers the next claimed input after that bound is
full, the plugin durably records an overflow posture and requests a typed
fail-closed pause. It does not drop the input or relabel it as related work;
native terminal recovery preserves the immutable original and capture order.

After a fresh related verdict, the native controller appends the immutable original at the
next safe worker boundary; it remains subject to existing holds and review
barriers and is not duplicated as plugin-generated steering. Deferred input is
a broker-owned task projection. The plugin journals its exact current-run set
and, after verified completion, sends all open IDs once in application-chosen
ordinal order through `continuation_input_ids`. The broker rejects omissions,
duplicates, foreign inputs, or completion while queued/ready input remains.
UI and command provenance remain quality context, never security
authentication.

Input captured while a completion watchdog, verifier, or completion handoff is
in flight first durably invalidates that conclusion and candidate. Only after
that append may the plugin cancel the obsolete child and release its exact
quality hold; the broker's queued/reviewing fence remains in force throughout.
The worker must later make a new explicit current-anchor completion request, so
neither ready nor deferred input can remain hidden behind an earlier completion
decision.

The signed application also declares three model-facing tools:
`supervise__report_progress`, `supervise__request_pivot`, and
`supervise__request_completion`. Their strict input contracts and policy
effects are documented in [MODEL-TOOLS.md](MODEL-TOOLS.md). Calls are rejected
unless a persistent instance has an exact selected session-scoped contract and
the caller repeats its exact current plan anchor. Evidence and requests update
only the application's bounded journal state; they do not grant authority or
prove completion.

Completion is a two-review quality gate. The plugin first requires every
contract criterion and the exact current plan completion step. On the first
committed turn at that step it acquires a hold and asks the generic native
verification controller to run the operator's configured suite against that
exact worker version and Git tree. The request is made while the source
`session.turn_committed` event is still pending; native execution cannot claim
it until the callback ACK is durable. The contract's `verification` strings are
quality requirements and evidence guidance. They are never converted into
commands or treated as executable authority; the operator's `[verify].commands`
may differ or be empty.

The targeted terminal event contains only strict factual result data: exact
source anchor, suite/command/result digests, typed outcome/failure facts, and
immutable evidence references. It contains no command text, output, routing
identity, timestamps, or completion decision. `commands_succeeded` makes those
facts eligible for a later completion claim. `command_failed` returns bounded
factual correction context; infrastructure failure or cancellation pauses
without inferring quality. `no_suite` records that no native suite existed. It
is not a pass: the candidate may proceed only to the fresh semantic reviews,
whose prompt explicitly carries the absent-suite fact and must never claim that
baseline verification prose ran. Stale or operator-invalidated results are
discarded without steering, pausing, or approving a different anchor.

After that factual boundary, the plugin holds the worker while the watchdog
reviews the explicit completion candidate and a fresh, separately token-bounded
read-only agent independently verifies the same contract, tree, anchor, and
immutable evidence. Only that verifier's current `approve` verdict marks the
application run complete. A parsed verdict survives later provider cleanup
failure. Host terminal metadata journals watchdog and verifier
input/output/cache counters separately, records incomplete accounting and
bounded cleanup fingerprints as diagnostics, and contains no USD policy. A
stale approval never completes the run.

The default `verifier_profile` is `required`: failure to obtain a valid verdict
pauses while retaining the completion candidate and hold. `advisory` instead
invalidates the candidate and resumes work. After approval the plugin first
persists its anchored verdict, calls the generic `stado_session_complete`
success transition with a stable run-derived idempotency key, and only then
CAS-releases the exact hold version. A crash at either boundary replays the same
completion or release instead of minting a second transition. Pause and stop
are never used as success aliases. Renewal ends only after the successful
handoff and exact release both succeed, not merely because the plugin recorded
its own verifier result.

The native-suite boundary is restart-safe as well. The plugin journals the
exact turn/worker intent, acquires the hold, and issues a stable idempotent
request before ACK. It journals terminal facts before releasing the exact hold
or requesting a pause. A callback timeout therefore replays the same request or
same pending effect rather than duplicating policy. Native command execution is
at-least-once across the irreducible command-finished/before-terminal-WAL crash
window; only digests and factual outcomes cross back into the plugin.

## Reproducible unsigned build

```sh
GOCACHE=/tmp/stado-plugins-go-cache go test ./...
SUPERVISE_BUILD_DIR=/tmp/supervise-build ./build.sh
./check.sh
```

The build produces `plugin.wasm` and a digest-filled unsigned manifest in
`SUPERVISE_BUILD_DIR` (default `dist/`). It does not produce a signature.
`check.sh` keeps both reproducibility builds in temporary directories and also
builds the plugin-owned evaluator, validates all six scenarios, and scores the
example observation pair. See [evals/README.md](evals/README.md).

Current stado's signed command dispatcher routes `/supervise` through the fixed
`stado_plugin_command` export. The signed command has an independent 15-minute
ceiling so the ordinary `stado_ui_choose` setup sequence and later proposal
render/confirmation callbacks can complete without extending the five-second
hook/event/tick deadline. The fresh bounded baseline agent runs asynchronously
between those durable commands. The export accepts
`/supervise [start] [objective]`, `status`, `resume`, and `cancel`; no contract JSON shortcut or
native setup fallback exists. Command origin and `stado_ui_*` are UI transport,
not security authentication. See
EP-0064 for persistent application composition, EP-0063 for plugin-defined
artifact kinds, and EP-0059 for the separate trusted-presenter activation
boundary that this private quality configuration does not cross.

## Current integration status

The pure policy and WASI module build now. Current stado supplies durable event
delivery, authenticated generic turn and child-terminal facts, strict-live
barrier ordering, persistent command/tool dispatch, the broker application
bridge, pinned read-only reviewer instances, and scheduling enforcement. TUI
is the only surface that currently owns the complete persistent lifecycle-
application composition; other session surfaces fail closed when such an
application is configured. The plugin fails closed at that seam rather than
inventing native supervise policy or a filesystem shortcut. See
[ABI-GAPS.md](ABI-GAPS.md) for the exact list.
