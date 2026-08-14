# Supervise model tools

The signed manifest is the canonical JSON Schema. All three tools use stado's
standard `stado_tool_<name>` export ABI, reject unknown/trailing JSON, and are
state-mutating only inside the persistent lifecycle application. A call needs:

- an `idempotency_key` matching `^[A-Za-z0-9._:-]{1,64}$`;
- the exact current `anchor` object (`session_sequence`, `plan_version`,
  `active_step`, `tree_digest`, and `turn_ref`);
- the exact session-scoped supervision contract candidate selected and
  journalled by that instance.

Each declaration opts into the generic `application_worker` projection with
`plan_visible:true`. The exact active worker therefore receives these workflow
controls in both Do and Plan modes, despite their state-mutating class; BTW and
unrelated sessions receive none. The host still applies every global tool
ceiling and rechecks the exact application/run owner when a call executes.

The application injects the current contract/anchor context at `pre_llm` only
after it has received an authenticated committed-turn anchor. Before then the
tools return an error and do not mutate state.

Independent review claims are serialized. A step-completion or final-completion
claim is rejected while another review is pending; it is never silently queued
behind an unrelated verdict or allowed to inherit that verdict's hold cleanup.
Evidence-only progress reports may still be recorded. A stale step verdict
cannot advance the plan and clears the old claim so the worker can submit a
fresh exact-anchor claim.

## `supervise__report_progress`

```json
{
  "idempotency_key": "criterion-0-compile",
  "anchor": {
    "session_sequence": 30064771073,
    "plan_version": 1,
    "active_step": "implement",
    "tree_digest": "0123456789abcdef",
    "turn_ref": "git:refs/stado/trees/example@0123456789abcdef#turn-7-iteration-1"
  },
  "criterion_index": 0,
  "evidence_refs": ["git:refs/stado/trees/example@0123456789abcdef"],
  "complete_active_step": "implement"
}
```

`complete_active_step` is optional. When present it must equal the anchor's
exact ordered-plan step. `criterion_index` identifies criterion evidence and is
deliberately independent of that plan position. The plugin records evidence
immediately but advances the plan only after a current-anchor reviewer actually
reviews the resulting `step_completion_claim` and approves it.

## `supervise__request_pivot`

```json
{
  "idempotency_key": "pivot-after-verification",
  "anchor": {
    "session_sequence": 30064771073,
    "plan_version": 1,
    "active_step": "implement",
    "tree_digest": "0123456789abcdef",
    "turn_ref": "git:refs/stado/trees/example@0123456789abcdef#turn-7-iteration-1"
  },
  "rationale": "The verification evidence disproves the original premise.",
  "replacement": {
    "objective": "Implement the bounded change",
    "constraints": ["Keep the existing public boundary"],
    "non_goals": ["Unrelated cleanup"],
    "acceptance_criteria": ["The bounded behavior works", "Regression coverage passes"],
    "plan": [
      {"id": "alternate", "title": "Use the verified alternate", "done_when": "implementation evidence exists"},
      {"id": "verify", "title": "Verify the alternate", "done_when": "declared verification passes"}
    ],
    "definition_of_done": ["Every criterion has current host evidence"],
    "verification": ["go test ./..."],
    "risks": ["The alternate may expand scope"]
  }
}
```

Rationale is 1..4096 bytes. `replacement` is a complete strict baseline, not a
free-form instruction. The plugin compares every non-plan field with the exact
selected contract and classifies the proposal itself. Only an exact plan-only
replacement may be selected from a current watchdog approval, and only when
setup configured `pivot_approval:watchdog`. Plan-only proposals under `user`
policy and every objective, constraint, non-goal, criterion, definition-of-done,
verification, or risk change wait for `/supervise resume` and an explicit
quality confirmation.

The plugin journals the exact reviewed digest and expected artifact version,
then uses `stado_artifact_edit` to create a new candidate version. Reply loss is
recovered by querying the deterministic next version before retrying the CAS.
Selection increments the application-owned plan version, re-anchors at the
first replacement step, invalidates old completion and verification state, and
keeps criterion evidence only where the exact criterion remains unchanged at
the same index. The worker recurrence keeps its original broker identity while
`pre_llm` supplies the selected replacement. Reviewer output, command origin,
and UI confirmation are quality-workflow inputs; none grants OS authority or a
new plugin capability.

## `supervise__request_completion`

```json
{
  "idempotency_key": "completion-after-tests",
  "anchor": {
    "session_sequence": 30064771073,
    "plan_version": 1,
    "active_step": "completion",
    "tree_digest": "0123456789abcdef",
    "turn_ref": "git:refs/stado/trees/example@0123456789abcdef#turn-7-iteration-1"
  },
  "criteria": [
    {"criterion_index": 0, "evidence_refs": ["git:diff:implementation"]},
    {"criterion_index": 1, "evidence_refs": ["verification:go-test"]}
  ]
}
```

Criterion indexes must be unique and inside the selected contract. Each evidence
list has 1..32 unique, nonblank references of at most 1024 bytes. Previously
recorded evidence counts toward coverage, but every contract criterion must be
covered, every approved plan step must be complete, and the active step must be
`completion` before the plugin records a candidate. Each verification entry in
the contract is descriptive quality/evidence prose, not executable command
authority. The plugin never hashes that prose into a command request. At the
exact completion turn it has already requested the operator-configured native
suite for the current worker version and Git tree. That suite may differ from
the contract prose or contain no commands.

Only an exact terminal `commands_succeeded` or `no_suite` fact admits the claim
to semantic review. The latter means “the operator configured no native
suite,” never “verification passed.” That fact is preserved explicitly in the
completion candidate and independent-verifier prompt. Command failure returns
the worker to correction; infrastructure failure or cancellation pauses;
stale/obsolete results cannot admit, steer, or pause the current tree.

The plugin then acquires a durable hold and schedules watchdog review. A
watchdog approval starts a fresh, separately token-bounded read-only verifier
over the exact contract, anchor, pinned tree, criterion evidence, bounded
immutable evidence references, and factual native-suite outcome. Only a current
`approve` from that verifier marks the application workflow complete; stale
approval is discarded, and a valid parsed verdict is not erased by later
provider cleanup failure. Completion does not itself grant authority. The
plugin durably hands a current verifier approval to the generic
`stado_session_complete` success transition, then CAS-releases the exact renewed
hold. Stable idempotency makes both operations restart-safe; neither pause nor
stop is translated into success.

Identical retries return `idempotent_replay:true` without another journal or
review action. Reusing a key with different input is rejected. The ledger is
bounded to 128 receipts per run; exhaustion fails closed rather than silently
discarding replay protection.
