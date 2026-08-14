# Terminal child facts

Supervise subscribes to the generic, host-published `agent.down` event defined
by EP-0064. It accepts exactly `stado.dev/agent-down-facts/v1`; unknown fields,
unknown terminal statuses, negative counters, malformed digests, and trailing
JSON fail closed.

Parent identity is not part of `event.data`. It comes only from the
authenticated `stado.dev/lifecycle/v1` envelope:

```json
{
  "anchor": {
    "session_id": "parent-session",
    "session_generation": 7,
    "canonical_repo_id": "repo-id"
  },
  "event": {
    "broker_sequence": 41,
    "evidence_refs": [
      "git:refs/sessions/child-session/tree@0123456789abcdef0123456789abcdef01234567",
      "git:refs/sessions/child-session/trace@89abcdef0123456789abcdef0123456789abcdef"
    ],
    "data": {
      "schema": "stado.dev/agent-down-facts/v1",
      "child": {
        "agent_id": "fleet-control-handle",
        "session_id": "child-session",
        "status": "completed",
        "role": "explorer",
        "mode": "read_only",
        "execution": "wait"
      },
      "budget": {
        "token_limit": 16384,
        "turn_limit": 4,
        "timeout_seconds": 120
      },
      "terminal": {
        "usage": {
          "input_tokens": 1200,
          "output_tokens": 300,
          "cache_read_tokens": 50,
          "cache_write_tokens": 10
        },
        "usage_complete": true,
        "cleanup": {
          "kind": "provider_close",
          "fingerprint": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
        }
      },
      "scope": {
        "ownership": "supervise",
        "write_paths": [],
        "violations": []
      },
      "changes": {
        "fork_tree_digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
      }
    }
  }
}
```

`child`, `child.agent_id`, `child.session_id`, `budget`, `terminal`,
`terminal.usage`, `terminal.usage_complete`, and `scope` are required. Optional
token counters default to zero. Optional path
sets carry at most 64 entries, violation sets at most 32, and every digest or
fingerprint is a lowercase `sha256:` value. `changes` may additionally carry
`changed_paths`, `changed_paths_digest`, and `changed_paths_truncated`;
`scope` may carry the corresponding write-path and violation digest/truncation
fields. `failure`, when present, contains only a bounded fingerprint. Raw
provider errors, local paths, parent identity, and application conclusions are
not host facts.

The envelope evidence references must be unique immutable tree or trace
coordinates for the exact terminal child. Change facts require the child tree
reference. Supervise records every bounded terminal observation, but it binds
one to policy only when the agent control handle exactly matches the pending
reviewer or verifier, or the exact review ID/ownership of a durably claimed
operator input.
The distinct child session ID binds immutable tree/trace evidence. For that
child it also compares the reported role, mode, execution, ownership,
admitted budget, and empty write scope with the signed spawn request.
A mismatch, scope violation, or changed path makes the semantic result invalid;
this conclusion is plugin policy and is never added to the host fact shape.

Token counters are accumulated exactly once from the durable `agent.down`
observation, separately for watchdogs and final verifiers. Polling supplies the
child's semantic JSON output and must return identical terminal metadata. The
plugin waits when polling wins the race with event delivery. Incomplete usage,
a non-completed status, a failure fingerprint, and cleanup are diagnostics. A
valid, current verdict still applies when cleanup later fails; cleanup cannot
replace or erase semantic output.
