# `get_network_requests` drains the buffer by default

## Summary

`get_network_requests` consumes (clears) the captured buffer unless `peek:
true` is passed explicitly. Most calls during exploratory work want to read
without losing entries — so the safe default is the opposite.

## Reproduction

1. Trigger some traffic.
2. `get_network_requests({limit: 5})` → returns 5, **buffer cleared**.
3. `get_network_requests({limit: 5})` again → empty (or only entries that
   arrived between the two calls).

## Current behavior

Default is destructive. The schema describes `peek` as `default false`.

## Expected behavior

One of:

- Flip the default: `peek: true` by default; explicit `peek: false` (or a
  separate `drain` tool) to consume.
- Replace `peek` with explicit `mode: "peek" | "drain"` so neither is
  defaulted silently.
- Keep current behavior but highlight in the description that calling this
  tool destroys data, and suggest `peek: true` for inspection.

The worry with the current default is that the LLM doesn't get to see the
side effect of its tool call: a "drained the buffer" notice in the response
would help it self-correct.

## Workaround used

Always pass `peek: true` and only set `peek: false` when explicitly clearing
between flow phases.

## Notes

Same pattern likely applies to `get_console_logs` and `get_js_errors` — worth
checking if they share the same surprise.
