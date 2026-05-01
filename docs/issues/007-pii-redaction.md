# Sensitive headers and cookies are echoed verbatim in tool output

## Summary

`get_cookies`, `get_network_requests`, `get_response_body`, and any tool
that surfaces request headers will return cookie values, `Authorization`
headers, and any other secret material verbatim. The LLM has no built-in
opt-in or opt-out for redacting these. They land in the conversation
transcript, in any auto-spilled tool-result files on disk, and in any
telemetry the LLM host has enabled.

For an MCP that's specifically pitched at giving an LLM access to a real
browser session (cookies, sessions, profiles), this is a real attack surface
even with a well-intentioned operator.

## Reproduction

1. Launch a browser with a profile that has any logged-in site (Gmail,
   GitHub, banking, anything).
2. Navigate to that site.
3. `get_cookies({urls: ["https://that-site.example/"]})` → cookies
   returned verbatim.
4. `get_network_requests` → every authenticated request includes the
   `Authorization: Bearer ...` header verbatim, plus `Cookie:` if applicable.

## Current behavior

No filter. Whatever Chrome holds is what the tool returns.

## Expected behavior

Layered controls, opt-in:

- CLI flag `--redact-headers Authorization,Cookie,Set-Cookie,X-CSRF-Token,...`
  that masks those header values in tool output (`<redacted>` or a stable
  hash so the LLM can still correlate without seeing the value).
- CLI flag `--redact-cookies` that returns cookie names + metadata
  (domain, expiry, http_only, secure) but masks `value`.
- Per-tool opt-in to override redaction when the LLM explicitly asks
  (`include_secrets: true`), so the safe default is silent and the unsafe
  case requires an obvious tool argument the operator can audit in logs.
- Document the privacy posture clearly in the README, including the fact
  that auto-spilled tool-result files on disk contain unredacted data
  unless redaction is configured.

## Workaround used

None at the MCP level. In this session I noted to the user when sensitive
data was about to land in the transcript, and asked them to acknowledge
before continuing. That's a manual mitigation that depends on the LLM
remembering to do it.

## Notes

Tool-result auto-spill files (when results exceed token limits) are written
to `~/.claude/projects/<workspace>/.../tool-results/*.txt`. Operator should
know that redaction at the MCP level also needs to flow through to those
spill files — otherwise grepping disk recovers everything that was supposed
to be hidden.
