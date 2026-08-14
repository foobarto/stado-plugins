# `session.turn_committed` facts v1

The host delivers one strict, bounded observation object as the `data` of a
`session.turn_committed` lifecycle event. The schema identifier is
`stado.dev/session-turn-facts/v1`.

This is a fact surface, not a supervision verdict surface. The host authenticates
the turn/tree anchor and reports what happened. It does not report that work is
stalled, in scope, risky, a plan pivot, criteria progress, or acceptable
completion.

## JSON Schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "stado.dev/session-turn-facts/v1",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema", "anchor", "provider_tokens", "assistant"],
  "properties": {
    "schema": {"const": "stado.dev/session-turn-facts/v1"},
    "anchor": {
      "type": "object",
      "additionalProperties": false,
      "required": ["session_sequence", "turn_ref", "tree_digest"],
      "properties": {
        "session_sequence": {"type": "integer", "minimum": 1},
        "turn_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        "tree_digest": {"type": "string", "minLength": 1, "maxLength": 512}
      }
    },
    "tool_outcomes": {
      "type": "array",
      "maxItems": 128,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "tool", "call_digest", "args_digest", "result_digest", "outcome"],
        "properties": {
          "id": {"type": "string", "minLength": 1, "maxLength": 256},
          "tool": {"type": "string", "minLength": 1, "maxLength": 256},
          "class": {"type": "string", "maxLength": 64},
          "call_digest": {"type": "string", "minLength": 1, "maxLength": 512},
          "args_digest": {"type": "string", "minLength": 1, "maxLength": 512},
          "result_digest": {"type": "string", "minLength": 1, "maxLength": 512},
          "outcome": {"enum": ["success", "error", "denied", "cancelled"]},
          "error_fingerprint": {"type": "string", "maxLength": 512},
          "evidence_refs": {"$ref": "#/$defs/evidenceRefs"}
        }
      }
    },
    "provider_tokens": {
      "type": "object",
      "additionalProperties": false,
      "required": ["input_tokens", "output_tokens"],
      "properties": {
        "input_tokens": {"type": "integer", "minimum": 0},
        "output_tokens": {"type": "integer", "minimum": 0},
        "cached_tokens": {"type": "integer", "minimum": 0},
        "budget_tokens": {"type": "integer", "minimum": 0}
      }
    },
    "verification_facts": {
      "type": "array",
      "maxItems": 64,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "command_digest", "result_digest", "outcome"],
        "properties": {
          "id": {"type": "string", "minLength": 1, "maxLength": 256},
          "command_digest": {"type": "string", "minLength": 1, "maxLength": 512},
          "result_digest": {"type": "string", "minLength": 1, "maxLength": 512},
          "outcome": {"enum": ["pass", "fail", "error", "cancelled"]},
          "evidence_refs": {"$ref": "#/$defs/evidenceRefs"}
        }
      }
    },
    "tree_diff": {
      "type": ["object", "null"],
      "additionalProperties": false,
      "required": ["before_digest", "after_digest", "diff_ref", "diff_digest"],
      "properties": {
        "before_digest": {"type": "string", "minLength": 1, "maxLength": 512},
        "after_digest": {"type": "string", "minLength": 1, "maxLength": 512},
        "diff_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        "diff_digest": {"type": "string", "minLength": 1, "maxLength": 512},
        "changed_paths": {
          "type": "array",
          "maxItems": 64,
          "items": {"type": "string", "minLength": 1, "maxLength": 512}
        },
        "bytes": {"type": "integer", "minimum": 0, "maximum": 65536},
        "evidence_refs": {"$ref": "#/$defs/evidenceRefs"}
      }
    },
    "assistant": {
      "type": "object",
      "additionalProperties": false,
      "required": ["message_ref", "digest"],
      "properties": {
        "message_ref": {"type": "string", "minLength": 1, "maxLength": 512},
        "digest": {"type": "string", "minLength": 1, "maxLength": 512},
        "excerpt": {"type": "string", "maxLength": 4096}
      }
    }
  },
  "$defs": {
    "evidenceRefs": {
      "type": "array",
      "maxItems": 32,
      "items": {"type": "string", "minLength": 1, "maxLength": 1024}
    }
  }
}
```

`tree_diff.after_digest` must equal `anchor.tree_digest`. Changed paths must be
normalized repository-relative paths with no absolute or `..` traversal.
Unknown fields, unknown enum values, trailing JSON, and unknown schema versions
are rejected.

`turn_ref` is host-minted as
`git:<tree-ref>@<tree-head-commit>#turn-N-iteration-M`; it never fabricates a
per-turn ref. It is the immutable source selector reviewer admission must
consume as `source:{at:<turn_ref>}` with no session ID. The host derives and
ancestry-authorizes the logical session, validates the commit, and pins that
tree synchronously before asynchronous admission. The plugin rejects
`last_committed_turn`, `turns/N`, and current-tip fallbacks.

`input_tokens` is the provider's per-turn total input count. `cached_tokens`, when
available, is an informational subset of that total and is not added a second
time. `budget_tokens` is the current host-enforced worker-run ceiling, not
per-turn spend. The plugin durably accumulates input and output tokens
from each committed turn before comparing them with that ceiling.

## Policy derived by the plugin

The supervise application converts these observations into its private event
history:

- tool outcome/fingerprint/digests drive repeated-failure and retry-thrash
  policy;
- verification chronology drives regression policy;
- changed paths are compared with the approved contract's
  `allowed_path_prefixes`, and diff size thresholds remain plugin policy;
- host evidence references are counted uniquely, but evidence activity does not
  reset the four-turn criteria-progress window;
- plan version, active step, and completed-step count are application state,
  never host turn fields;
- tool class/name and the bounded assistant excerpt may trigger application
  risk/pivot review policy;
- provider token fields feed token-only budget policy. No cost field exists.

The per-turn `verification_facts` above are observations for regression and
retry policy. They do not certify completion and are not interpreted as the
contract's descriptive `verification` strings. Completion-time native suite
execution uses the separate asynchronous `session:verification:request` /
`session.verification_finished` contract, anchored to a still-pending committed
turn. The application receives only digests, typed outcomes, and immutable
evidence references—never commands or output.

The host fact shape deliberately has no completion/finality field. Assistant
prose, a no-tool turn, or ordinary turn finality cannot create completion
policy. Only the plugin's explicit, exact-anchor
`supervise__request_completion` tool can create the durable candidate that
enters watchdog and verifier review.

The application composes the host turn/tree anchor with its own approved-plan
version before binding a reviewer verdict. Neither side can silently stand in
for the other.

`testdata/session-turn-facts-v1.json` is copied byte-for-byte from stado's
`internal/runtime/testdata` fixture. Both repositories decode it in tests so a
producer/consumer shape drift fails before release.
