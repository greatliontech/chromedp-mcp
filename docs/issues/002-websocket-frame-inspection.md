# No WebSocket frame inspection

## Summary

`get_network_requests` accepts `type: "websocket"` as a filter but returns
empty even when the page has open WebSocket connections. There is no way to
list active sockets, see their URLs, or read frames sent/received.

## Reproduction

1. Open a page known to use WebSockets (any modern SPA with a real-time
   feature — chat, presence, live tracking, collaborative editor).
2. `get_network_requests({type: "websocket"})` → empty array.
3. `get_network_requests` (no filter) → no entries with `type: "websocket"`
   visible either.

For a concrete check: Wolt's CSP allows `wss://*.wolt.com` and the order-
tracking page advertises live updates, but their tracking is implemented as
HTTP polling rather than WS — so we can't tell whether the absence of WS
entries is "Wolt isn't using WS here" or "MCP doesn't observe WS." Both end
up looking identical to the LLM.

## Current behavior

WebSockets are invisible to the MCP. No tools surface them.

## Expected behavior

CDP exposes the relevant events:
- `Network.webSocketCreated` — URL, request id
- `Network.webSocketFrameSent` — frame payload
- `Network.webSocketFrameReceived` — frame payload
- `Network.webSocketClosed`

Minimum viable surface:

- `get_network_requests` includes WS connections as entries with
  `type: "websocket"`, `url`, and a `frames_sent_count` / `frames_received_count`.
- New tool `get_websocket_frames(request_id, {direction: "sent"|"received", limit, peek})`
  returning frames with payload (string or base64).

## Workaround used

For Wolt I confirmed via `performance.getEntries()` that no `wss://` entries
existed and from the SPA's polling cadence I could rule out a WS-based
implementation. For an SPA that *does* use WS, the only workaround is
manual: use Chrome DevTools UI yourself, outside the MCP.

## Notes

Worth designing for: WS frames can be high-volume. A peek/drain pattern with
a per-socket buffer is probably wise, mirroring the HTTP request buffer.
