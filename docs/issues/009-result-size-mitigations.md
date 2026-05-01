# Tools that return large payloads have no server-side size controls

## Summary

Several tools return responses that routinely exceed the MCP host's
per-message token budget, triggering host-side spill-to-file behavior. The
LLM then has to shell out to `jq` to consume what could have been bounded
server-side. The spill itself isn't chromedp-mcp's problem, but the absence
of size knobs on the tools that produce big responses is.

## Reproduction

A single browsing session against a real production SPA (Wolt) hit the
spill threshold in this session for:

- `evaluate` returning a 234 KB string (full venue assortment grabbed via an
  in-page `fetch` then returned wholesale).
- `evaluate` returning a 1.1 MB string (`JSON.stringify(window.__captured)`
  for accumulated network entries).
- `get_response_body` returning a 50 KB JSON response.
- `get_network_requests` returning the full request-headers payload for
  every entry, ballooning a 30-entry result over the threshold even when
  the LLM only wanted method+URL+status.

For each of these the LLM resorts to a `jq` pipeline against the host's
auto-spilled file, costing turns and shell calls.

## Current behavior

- `get_response_body(request_id)` returns the full body. No range/slice.
- `get_network_requests({limit, peek, ...})` returns the full request-headers
  and response-headers blocks for every entry. No way to ask for a compact
  shape.
- `evaluate({expression})` returns whatever the expression evaluates to.
  No `max_result_bytes` knob.

The host catches the oversized result and spills to file as a fallback,
which works but pushes complexity into the LLM's tool plan.

## Expected behavior

Three independent additions, any of which would help:

- **`get_response_body`**: accept `range: {start, end}` (byte offsets) and
  return a `total_bytes` field so the LLM can chunk explicitly. Useful for
  very large JSON responses where the LLM only needs the first KB to know
  the shape.
- **`get_network_requests`**: accept `fields: ["url","method","status",...]`
  to project only the requested fields. Default could remain "everything"
  for compatibility; opt-in projection lets cost-aware callers stay tiny.
  Also consider a `compact` boolean that drops both header blocks and
  returns just `{id, method, url, status, size, type, ts}`.
- **`evaluate`**: accept `max_result_bytes` (e.g. 65536) and truncate the
  serialized result with a marker (`"<...truncated, total_bytes=N>"`) when
  it exceeds. Lets the LLM ask "is this big?" before asking "give me
  everything."

These are independent and any one of them would reduce spill frequency
significantly.

## Workaround used

For each oversized response, the host's spill file was processed via a
`jq` shell call. Works but breaks "look at the response and act on it" into
multiple turns.

## Notes

The host's spill behavior is genuinely useful as a safety net — without it
the LLM would just blow up on token-limit errors. The opportunity here is
to give the LLM a way to *avoid* triggering it in the first place by being
explicit about what it wants.
