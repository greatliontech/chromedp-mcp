# chromedp-mcp Design Document

An MCP server that gives LLMs a fully-featured headless browser for inspecting, debugging, interacting with, and profiling web applications.

## Overview

```
                                                          CDP (WebSocket)
                  MCP (stdio)         +----------------+  ────────────────>  +---------+
+-------------+ ◄──────────────────>  |  chromedp-mcp  |                    | Chrome  |
|  LLM / Host |   tools              |                |  <────────────────  | Browser |
+-------------+                       |  - browser mgr |  events, results   +---------+
                                      |  - tab mgr     |
                                      |  - collectors   |
                                      +----------------+
```

The server exposes MCP tools that map to browser operations. Under the hood, `chromedp` drives Chrome via the Chrome DevTools Protocol. The server captures console logs, network traffic, JS errors, and performance data in background collectors, making them available to the LLM on demand.

## Transport

stdio only. This is the standard for CLI-integrated MCP servers (Claude Desktop, Claude CLI, etc.).

## Browser Lifecycle

The browser lifecycle is entirely tool-driven. The LLM decides when to launch or connect to a browser, and with what settings. The only CLI-level configuration that affects browser launches is `--allowed-profiles` (which gates access to real Chrome user profiles) and `--user-data-dir` (which overrides where profiles are discovered). See [User Profile Security](#user-profile-security).

This means the LLM can:
- Launch a headless browser for automated testing
- Connect to a developer's running browser to inspect a live session
- Switch between browsers mid-conversation
- Tear down and re-launch with different settings

### Browser Management Tools

#### `browser_launch`

Launch a new Chrome instance managed by the server. Connects at the browser CDP level and automatically attaches Chrome's initial default `"page"` target (or restored profile tabs, falling back to opening one tab if none attach) as a managed active tab, so `navigate` and tab tools work immediately without calling `tab_new` first.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `headless` | bool | no | Run in headless mode (default `true`) |
| `width` | int | no | Initial viewport width (default 1920) |
| `height` | int | no | Initial viewport height (default 1080) |
| `profile` | string | no | Chrome profile display name to use (e.g. `"Work"`). Must be in the allowed profiles list. Use `browser_list_profiles` to see available profiles. Requires `--allowed-profiles` to be configured. |

Returns: `{browser_id, tabs}` where `tabs` is the array of attached initial tabs (`{id, url, title, active}`).

When `profile` is specified, the browser is launched with `--user-data-dir` and `--profile-directory` pointing to the real Chrome profile. This gives the browser access to the profile's cookies, sessions, extensions, and other stored data. When omitted, a temporary ephemeral profile is used (default behavior).

Closing the server kills any launched browsers.

#### `browser_list_profiles`

List available Chrome/Chromium user profiles that can be used with `browser_launch`. Only registered when `--allowed-profiles` is configured. Returns only profiles that are in the allowed list.

No parameters.

Returns: `{profiles}` array of `{name, dir}` objects where `name` is the display name and `dir` is the profile directory name.

#### `browser_connect`

Connect to an already-running Chrome instance via its remote debugging URL. Connects at the browser CDP level without creating a blank tab, and automatically adopts all existing open `"page"` targets (skipping prerender targets, and skipping wedged or dialog-paused targets that time out during attach rather than blocking connection).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | yes | Chrome remote debugging URL (`ws://`, `wss://`, `http://`, or `https://`) |

Returns: `{browser_id, tabs}` where `tabs` lists the adopted open tabs (`{id, url, title, active}`).

The server does not own the browser lifecycle in this mode. Disconnecting via `browser_close` detaches from adopted user tabs (leaving them open in Chrome) and closes only tabs created by this server, without killing Chrome. Explicit `tab_close` closes the underlying Chrome target even for adopted tabs. Tabs closed externally in Chrome are automatically pruned from the tab manager.

#### `browser_activate`

Set a managed browser as the active browser by ID.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | yes | Browser ID to set as active |

#### `browser_close`

Close a browser. In launch mode, kills the Chrome process. In connect mode, disconnects.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. If omitted, closes the active browser. |

Returns: `{closed}` with the closed browser ID.

#### `browser_list`

List all active managed browsers (pruning any that exited externally).

No parameters.

Returns: `{browsers}` array of `{id, mode, active, tabs}` objects (where `tabs` is the open tab count).

### Active Browser

When multiple browsers are managed, one is the **active browser**. Tab tools that don't specify a browser operate on the active browser. The most recently launched/connected browser becomes active. If the active browser is closed, the most recently used remaining browser becomes active.

## Tab Model

The server supports multiple concurrent tabs per browser. Each tab is identified by a string **tab ID** returned on creation. Most tools accept an optional `tab` parameter; when omitted, the tool operates on the **active tab** of the active browser.

Internally, each tab maps to a `chromedp` target `context.Context` derived from the browser context. Creating a tab creates a new Chrome target; closing a tab cancels its context.

### Tab Management Tools

#### `tab_new`

Create a new tab, optionally navigating to a URL.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | no | URL to navigate to |
| `browser` | string | no | Browser ID. Defaults to active browser. |

Returns: `{tab_id, url?}`. The new tab becomes the active tab.

#### `tab_list`

List all open tabs with their IDs, URLs, and titles.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. Defaults to active browser. |

Returns: `{tabs}` array of `{id, url, title, active}` objects.

#### `tab_activate`

Set a tab as the active tab.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | yes | Tab ID |

#### `tab_close`

Close a tab.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | yes | Tab ID |

If the active tab is closed, the most recently used remaining tab becomes active.

### Explicit Browser and Tab Lifecycle

No implicit auto-creation. If no browser exists when a tool that requires one is called, an error is returned telling the LLM to call `browser_launch` or `browser_connect`. `browser_launch` attaches Chrome's initial default tab (and `browser_connect` adopts existing open tabs) so tab-scoped tools work immediately; if all tabs are later closed (or a connected browser has zero open tabs), calling a tab tool returns an error telling the LLM to call `tab_new`.

## Event Collectors

The server registers CDP event listeners per tab that buffer events. These start automatically when a tab is created.

### Collected Event Types

| Collector | CDP Events | Buffer Behavior |
|-----------|-----------|-----------------|
| **Console** | `runtime.EventConsoleAPICalled` | Ring buffer (default 1000 entries). Normalizes CDP `"warning"` to `"warn"` and captures top stack frame `source` (`url:line:column`). |
| **JS Errors** | `runtime.EventExceptionThrown` | Ring buffer (default 500 entries) |
| **Network** | `network.EventRequestWillBeSent`, `network.EventResponseReceived`, `network.EventLoadingFinished`, `network.EventLoadingFailed` | Ring buffer of request/response pairs (default 1000 entries); preserves redirect chains, multi-value headers (joined with `\n`), failure details (`blocked_reason`, `cors_error`, `canceled`), and timing breakdown (`transfer_ms`, etc.). Response bodies captured lazily on demand. |
| **WebSocket** | `network.EventWebSocketCreated`, `network.EventWebSocketWillSendHandshakeRequest`, `network.EventWebSocketHandshakeResponseReceived`, `network.EventWebSocketFrameSent`, `network.EventWebSocketFrameReceived`, `network.EventWebSocketFrameError`, `network.EventWebSocketClosed` | Tracks up to 100 connections per tab with bounded sent/received frame ring buffers (500 frames per direction per socket) |
| **Performance Timeline** | `performancetimeline.EventTimelineEventAdded` | Ring buffers for layout shifts (default 200 entries) and LCP (default 50 entries) |
| **URL Changes** | `page.EventFrameNavigated`, `page.EventNavigatedWithinDocument` | Ring buffer (default 100 entries) used by inline activity observation |
| **Dialog** | `page.EventJavascriptDialogOpening`, `page.EventJavascriptDialogClosed` (plus crash/navigation/frame-detach clearing) | Tracks the single active JS modal dialog per tab without blocking on the renderer |
| **Downloads** | `browser.EventDownloadWillBegin`, `browser.EventDownloadProgress` | Browser-scoped ring buffer (default 200 completed/canceled entries) plus live in-progress tracking and async GUID-to-filename renaming |

Each collector supports two read modes, selected by the required `mode`
parameter on the corresponding tool:
- **`mode: "drain"`**: Return and remove only the entries that match the
  filter (and are within the limit). Non-matching entries and matches
  beyond the limit are retained, so successive calls paginate.
- **`mode: "peek"`**: Return matching entries without modifying the buffer.

`mode` is required at every call site so the destructive vs.
non-destructive choice is always explicit. WebSocket connections in
`get_network_requests` are long-lived and always returned by snapshot
regardless of `mode`.

Collectors also support filtering at read time (e.g., console level, network status code range).

## Tools

All tools return structured output via `CallToolResult`. Tools that produce images return `ImageContent` (PNG/JPEG). Tools that return data return `TextContent` with JSON.

Every tool that operates on a tab accepts an optional `tab` parameter (string tab ID). When omitted, the active tab is used.

### Navigation

#### `navigate`

Navigate to a URL.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | yes | The URL to navigate to |
| `tab` | string | no | Tab ID |
| `wait_until` | string | no | Wait condition: `"load"` (default), `"domcontentloaded"`, `"networkidle"` |

Returns: `{url, title, status?}` (final URL after redirects, page title, and HTTP status code when available).

**Implementation note**: `"networkidle"` uses Chrome's built-in `page.EventLifecycleEvent` with `Name == "networkIdle"` (0 in-flight requests for 500ms), not a custom heuristic.

#### `reload`

Reload the current page.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `bypass_cache` | bool | no | Bypass browser cache (default `false`) |

Returns: `{url, title}`.

#### `go_back` / `go_forward`

Navigate browser history.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

#### `wait_for`

Wait for a condition to be met.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to wait for (waits until element is visible) |
| `expression` | string | no | JS expression to poll until truthy |
| `timeout` | int | no | Timeout in milliseconds (default 30000) |

Exactly one of `selector` or `expression` must be provided.

The expression is wrapped in `Promise.resolve(EXPR).then(Boolean)`
server-side before polling. This means:

- A predicate whose terminal value is a non-primitive (e.g. an
  optional-chained DOM query that returns an element or DOMRect) is
  coerced to truthy/falsy rather than failing with CDP's "Object
  reference chain is too long".
- A predicate that evaluates to a Promise (`fetch(...).then(...)`,
  `(async () => ...)()`) is awaited; the resolved value drives
  termination. A bare `Boolean(somePromise)` would always be truthy
  without awaiting — `Promise.resolve(...).then(Boolean)` defends
  against that.
- A trailing line comment in the expression (`// note`) doesn't break
  the wrap because `EXPR` is sandwiched between newlines.

### Visual Feedback

#### `screenshot`

Capture a screenshot.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to screenshot a specific element. If omitted, captures the viewport. |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` to become visible (default `5000`). Only used when `selector` is set; image encoding runs outside the selector timeout. |
| `full_page` | bool | no | Capture the full scrollable page instead of just the viewport (default `false`). Ignored if `selector` is set. |
| `format` | string | no | `"png"` (default) or `"jpeg"` |
| `quality` | int | no | JPEG quality 1-100 (default 80). Ignored for PNG. |
| `max_dimension` | int | no | Maximum allowed width or height in physical pixels (accounting for device pixel ratio). If the capture exceeds this in either dimension, it is downscaled proportionally via CDP clip scaling. |
| `filename` | string | no | Save to disk with this filename (requires `--download-dir`). Omit to return inline only. The image is still returned inline. |

Returns: `ImageContent` with the screenshot. If `filename` is set and `--download-dir` is configured, also saves to disk and appends a `TextContent` with the file path.

#### `pdf`

Generate a PDF of the current page.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `landscape` | bool | no | Landscape orientation (default `false`) |
| `print_background` | bool | no | Include background graphics (default `true`) |
| `scale` | float | no | Page rendering scale (default 1.0) |
| `paper_width` | float | no | Paper width in inches (default 8.5) |
| `paper_height` | float | no | Paper height in inches (default 11) |
| `page_ranges` | string | no | Page ranges, e.g. `"1-5, 8"`. Defaults to all pages. |
| `filename` | string | no | Save to disk with this filename (requires `--download-dir`). Omit to return inline only. |

Returns: `EmbeddedResource` with the PDF as a blob (`application/pdf`). If `filename` is set and `--download-dir` is configured, saves to disk and returns a `TextContent` with the file path instead of the inline blob.

#### `set_viewport`

Set the browser viewport dimensions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `width` | int | yes | Viewport width in pixels |
| `height` | int | yes | Viewport height in pixels |
| `device_scale_factor` | float | no | Device scale factor (default 1.0) |
| `mobile` | bool | no | Emulate mobile device (default `false`) |

### DOM Inspection

#### `query`

Query the DOM and return information about matching elements.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional frame ID from `list_frames` to target a same-origin frame (including the main frame) |
| `selector` | string | yes | CSS selector |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `limit` | int | no | Max elements to return (default 10) |
| `attributes` | bool | no | Include element attributes (default `true`) |
| `computed_style` | []string | no | List of CSS property names to include computed values for |
| `outer_html` | bool | no | Include outer HTML (default `false`) |
| `text` | bool | no | Include text content (default `true`) |
| `bbox` | bool | no | Include bounding box dimensions (default `false`). Coordinates are relative to the targeted frame's viewport when `frame` is set. |

Returns: `{elements, total}` where `elements` is an array of `{index, tag_name, attributes?, text?, outer_html?, computed_style?, bbox?}` objects.

#### `get_html`

Get the HTML content of the page or a subtree.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional frame ID from `list_frames` to target a same-origin frame (including the main frame) |
| `selector` | string | no | CSS selector to scope to a subtree. If omitted, returns the full page. |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `outer` | bool | no | Return outer HTML (default `true`). If `false`, returns inner HTML. |

Returns: `{html}`.

#### `get_text`

Get the visible text content of the page or an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional frame ID from `list_frames` to target a same-origin frame (including the main frame) |
| `selector` | string | no | CSS selector. If omitted, returns text of the entire page body. |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `hidden` | bool | no | Include hidden text (`textContent`) instead of visible text (`innerText`, default `false`) |

Returns: `{text}`.

#### `list_frames`

List all frames in a tab, including the main frame, same-origin in-process iframes (`reachable: true`), and known out-of-process cross-origin iframes (`reachable: false`). Same-origin frame IDs can be passed to the optional `frame` parameter on `evaluate`, `query`, `get_html`, and `get_text`; cross-origin OOPIFs return a clear error. Fails fast if a JavaScript modal dialog is open (since `Page.getFrameTree` runs in the renderer). Note: because `Page.getFrameTree` omits OOPIFs and Chrome does not emit `frameDetached(remove)` on this session when an already-swapped OOPIF element is removed directly from the top frame, pre-existing OOPIFs on `browser_connect`-adopted tabs are not visible until navigation, and a directly removed top-level OOPIF persists in `list_frames` until top-frame navigation (descendant OOPIFs of a detached/navigated/swapped subframe are pruned immediately).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{frames}` array of `{id, parent_id?, name?, url?, security_origin?, is_main, reachable}` objects.

#### `get_accessibility_tree`

Get the accessibility tree of the page. This is a compact representation of the page structure that is much more token-efficient than raw HTML. Uses CDP's `accessibility.GetFullAXTree` — when scoped to a `selector`, roots the returned subtree at the matched element's `BackendNodeID` (falling back to `GetPartialAXTree` with `fetchRelatives: false` if the node is absent from the full tree) and applies `depth` and `interesting_only` consistently.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to scope to a subtree |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `depth` | int | no | Max tree depth below each root (`depth=1` includes the root and its direct children; `0` = unlimited) |
| `interesting_only` | bool | no | Filter to only "interesting" nodes — those with a role, name, or value (default `true`) |

Returns: `{tree}` array of accessibility nodes. Each node includes: `role`, `name`, `value`, `description`, `properties` (states like `focused`, `checked`, `expanded`, etc.), and `children`.

### Console & Errors

#### `get_console_logs`

Get captured console messages.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `level` | string | no | Open string filter matching any CDP console level (e.g. `"log"`, `"warn"`, `"error"`, `"info"`, `"debug"`, `"assert"`, `"trace"`). CDP `"warning"` is normalized to `"warn"`. If omitted, returns all. |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume matching entries) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{logs}` array of `{level, text, source?, timestamp}` objects (`source` is `url:line:column` from the top stack frame when available).

#### `get_js_errors`

Get captured JavaScript exceptions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume entries) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{errors}` array of `{message, source?, line, column, stack_trace?, timestamp}` objects.

#### `clear_console`

Clear the console and JS error buffers for a tab.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

#### `clear_buffers`

Clear one or more event collector buffers. Clears completed/historical buffered entries while preserving live/in-flight state: open WebSockets stay tracked (clearing their frame rings and frame counts and removing closed connections), in-progress downloads remain tracked and still rename on completion, in-flight HTTP requests remain tracked, the URL-change baseline is preserved, and live modal dialog state is untouched. Clearing only `["downloads"]` without `tab` succeeds even with zero open tabs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID. When provided without `browser`, `downloads` resolves to that tab's owning browser. |
| `browser` | string | no | Browser ID (defaults to active browser). When provided without `tab`, tab-scoped targets clear that browser's active tab. When both are provided, `tab` ownership is validated. |
| `targets` | []string | no | Subset of `"console"`, `"errors"`, `"network"`, `"websocket"`, `"performance"`, `"url_changes"`, `"downloads"`, `"all"`. Omit or pass `["all"]` to clear all buffers; an explicit empty array `[]` is rejected. |

Returns: `{cleared}` listing the cleared buffer names in canonical order.

### Network Inspection

#### `get_network_requests`

Get captured network requests.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries in HTTP buffer) or `"drain"` (consume matching entries). WebSocket connections are long-lived and always returned by snapshot regardless of mode. |
| `limit` | int | no | Max entries to return (default all) |
| `type` | string | no | Filter by resource type: `"document"`, `"stylesheet"`, `"script"`, `"image"`, `"xhr"`, `"fetch"`, `"websocket"`, `"other"` |
| `status_min` | int | no | Filter by minimum HTTP status code |
| `status_max` | int | no | Filter by maximum HTTP status code |
| `url_pattern` | string | no | Filter by URL substring match |
| `failed_only` | bool | no | Return only failed requests (default `false`) |
| `fields` | []string | no | Project each entry to this set of JSON keys (e.g. `["id","method","url","status"]`). When omitted or empty, every field is returned. Useful to keep responses tiny when listing many entries. Unknown field names produce an error. |

Returns: `{requests}` array of request objects with `{id, url, method, status?, type?, timing?, request_headers?, response_headers?, size?, error?, blocked_reason?, cors_error?, canceled?, failed?, start_time, end_time?, has_request_body?, request_body?, request_body_base64?, request_body_truncated?, frames_sent_count?, frames_received_count?}`. `timing` contains `{dns_lookup_ms?, connect_ms?, tls_ms?, ttfb_ms?, transfer_ms?, total_ms?}`. The frame counts are present only on WebSocket entries; the request-body fields are present only on entries that carried a request body. In `drain` mode, only matching HTTP entries within `limit` are removed so successive calls paginate.

#### `get_response_body`

Get the response body of a specific network request.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | The request ID from `get_network_requests` |
| `range_start` | int | no | Inclusive byte offset to start from (default `0`, must be `>= 0`). |
| `range_end` | int | no | Exclusive byte offset to stop at (default `0` = end of body, must be `0` or `>= range_start`). Past-the-end is clamped. When `base64_encoded=true`, offsets address the decoded binary. |

Returns: `{body, base64_encoded, total_bytes, truncated?}`. `total_bytes` is the full body size so the caller can plan paging; `truncated` is true when the returned body is a strict subset of the full body.

#### `get_request_body`

Get the full POST/PUT/PATCH/DELETE request body of a specific request. Use when `get_network_requests` reports `has_request_body=true` and the inline `request_body` was empty or truncated.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | The request ID from `get_network_requests` |
| `range_start` | int | no | Inclusive byte offset to start from (default `0`, must be `>= 0`). |
| `range_end` | int | no | Exclusive byte offset to stop at (default `0` = end of body, must be `0` or `>= range_start`). Past-the-end is clamped. |

Returns: `{body, base64_encoded, total_bytes, truncated?}`.

#### `get_websocket_frames`

Get WebSocket frames sent and/or received on a specific connection. Use the request ID returned for entries with `type=websocket` from `get_network_requests`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | The websocket connection's request ID from `get_network_requests` |
| `direction` | string | no | `"sent"`, `"received"`, or `"both"` (default `"both"`) |
| `limit` | int | no | Max frames to return per direction (default all buffered) |
| `mode` | string | **yes** | `"peek"` (keep frames in the buffer) or `"drain"` (consume them) |

Returns: `{sent, received}` arrays of frame objects with `{opcode, mask?, direction, payload?, payload_base64?, payload_truncated?, time}`. Text frames (opcode 1) carry UTF-8 payloads; other opcodes carry base64-encoded binary in `payload`.

### JavaScript Execution

#### `evaluate`

Execute JavaScript in the page context. Without `selector`, `expression` is evaluated directly as a JS expression (do not use a top-level `return`). When `selector` is provided, `expression` runs as the body of a function with the first matched element passed as `el` — use `return` to produce a value (e.g. `return el.textContent`).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional frame ID from `list_frames` to target a same-origin frame (including the main frame) |
| `expression` | string | yes | JavaScript expression to evaluate (or function body using `el` and `return` when `selector` is set) |
| `selector` | string | no | CSS selector. If provided, the first matched element is available as `el` in the expression. |
| `timeout` | int | no | Max time in milliseconds to wait for selector (default 5000). Only used when `selector` is set. |
| `await_promise` | bool | no | If the expression returns a Promise, wait for it to resolve (default `true`) |
| `max_result_bytes` | int | no | Cap the serialized result at this many bytes. When the JSON encoding exceeds the cap, `result` is replaced with the truncated prefix as a JSON string and `truncated`/`total_bytes` describe the cut. Default 0 (no cap). |

Returns: `{result, truncated?, total_bytes?}` where `result` is the JSON-encoded value produced by the expression (or — when truncated — a JSON string of the truncated prefix). If the evaluation throws a JS exception, the tool returns an MCP tool error (`IsError: true`).

### Interaction

The ten action tools in this section — `click`, `type`, `select_option`, `submit_form`, `scroll`, `hover`, `focus`, `press_key`, `upload_files`, `drag_and_drop` — accept an optional `observe_window_ms` parameter (`get_dialog` and `handle_dialog` do not). When it is greater than zero, the tool observes browser activity across the action and returns the counts in an `observed` object on its result. See [Observation](#observation) for the semantics of that object and why observation is inline rather than a standalone tool. All selector-based interaction tools also accept an optional `timeout` parameter (selector wait timeout in ms, default `5000`).

#### `click`

Click an element. Probes the target's interactive state before dispatching:
- Errors out (no click is sent) when the element has the `disabled` attribute or `aria-disabled="true"`. Both are almost always a bug in the caller's plan.
- Returns the post-resolution state plus an optional warning when the click was dispatched but likely had no effect (`pointer-events: none`, hidden, zero-size).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to click |
| `button` | string | no | Mouse button: `"left"` (default), `"right"`, `"middle"` |
| `click_count` | int | no | Number of clicks (default 1, use 2 for double-click) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after dispatch (default 0 = no observation) |

Returns: `{disabled, aria_disabled, pointer_events, visible, warning?, observed?}`. `warning` is omitted on a clean click and non-empty when the dispatch likely had no effect. `observed` is present only when `observe_window_ms > 0`.

The `warning` and `observed` fields answer different questions and are complementary: `warning` is a *pre-dispatch* prediction from the element's computed state ("this click probably won't do anything"), while `observed` is a *post-dispatch* measurement ("this click in fact did/didn't do anything"). A click can warn and still have an effect, or look clean and silently no-op.

#### `type`

Type text into an element matching a selector.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the input element |
| `text` | string | yes | Text to type |
| `mode` | string | **yes** | `"replace"` (clear field first, common case) or `"append"` (type after existing value, e.g. contenteditable mid-composition) |
| `delay` | int | no | Delay between keystrokes in milliseconds (default 0) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after typing (default 0). Catches autocomplete/typeahead requests. |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `select_option`

Select an option from a `<select>` element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the `<select>` element |
| `value` | string | no | Option value to select |
| `label` | string | no | Option visible text to select |
| `index` | int | no | Option index to select |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after the `change` event fires (default 0). Catches cascading dropdown loads. |

Exactly one of `value`, `label`, or `index` must be provided. For `<select multiple>` elements, each call adds to the selection (does not deselect existing options).

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `submit_form`

Submit a form. Uses `requestSubmit()`, so the `submit` event fires and JS handlers can intercept it.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the form or an element within the form |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after submit (default 0) |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

Observation is worth more here than on any other action. A form whose submit handler short-circuits — HTML5 constraint validation, a JS validation guard, a framework interceptor — fires the event and returns cleanly, so the tool reports success while nothing happened. `observed.has_any_effect = false` is the only signal that distinguishes a landed submit from a swallowed one.

#### `scroll`

Scroll the page or an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to scroll into view. If omitted, scrolls the page. |
| `x` | int | no | Horizontal scroll offset in pixels (when `selector` is omitted) |
| `y` | int | no | Vertical scroll offset in pixels (when `selector` is omitted) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`; only used when `selector` is set) |
| `observe_window_ms` | int | no | Observe activity for this many ms after scrolling (default 0). Catches lazy-load and infinite-scroll fetches. |

If `selector` is provided, scrolls the element into view. If omitted, scrolls the page by the specified offsets.

Returns: `{scroll_x, scroll_y, observed}`. `observed` is present only when `observe_window_ms > 0`.

#### `hover`

Hover over an element. Moves the CDP mouse cursor to the element's center, which activates both JavaScript event listeners (`mouseover`, `mouseenter`) and the CSS `:hover` pseudo-class.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to hover over |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after the hover (default 0). Catches tooltip/menu reveals. |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `focus`

Focus an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to focus |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after focusing (default 0). Catches validation reveal and autofill. |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `press_key`

Press a keyboard key.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `key` | string | yes | Key to press (e.g., `"Enter"`, `"Tab"`, `"Escape"`, `"ArrowDown"`) |
| `modifiers` | []string | no | Modifier keys: `"ctrl"`, `"shift"`, `"alt"`, `"meta"` |
| `observe_window_ms` | int | no | Observe activity for this many ms after the key event (default 0). Catches Enter-submit, Escape-dismiss, arrow navigation. |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `upload_files`

Set files on a file input element. For locally launched browsers (`mode == "launch"`), paths are validated against the local filesystem (must be absolute existing non-directory paths); for remote connected browsers (`mode == "connect"`), paths are passed directly to Chrome so remote/container filesystems work.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the file input element |
| `paths` | []string | yes | Non-empty file paths to set (validated as local absolute files on launched browsers) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Observe activity for this many ms after `input.files` changes (default 0). Catches the upload XHR an `onchange` handler typically fires. |

Returns: `{observed}` — present only when `observe_window_ms > 0`.

#### `get_dialog`

Inspect the currently open JavaScript modal dialog (`alert`, `confirm`, `prompt`, `beforeunload`) on a tab without blocking on the renderer.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{open, dialog?}`. When `open` is `false`, `dialog` is omitted. When `open` is `true`, `dialog` contains `{type, message, default_prompt?, url?, has_browser_handler, opened_at}`.

#### `handle_dialog`

Handle a JavaScript dialog (alert, confirm, prompt, beforeunload). Clears the tracked dialog generation synchronously on success so immediate follow-up `get_dialog` or observation calls do not race the async close event (without clobbering a chained subsequent dialog).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `accept` | bool | yes | Accept or dismiss the dialog |
| `text` | string | no | Text to enter in a prompt dialog. When accepted, omit `text` to submit the prompt's `default_prompt` value, or pass `""` to submit an empty string. |

#### `drag_and_drop`

Drag an element onto a drop target by CSS selector. Uses CDP `Input.setInterceptDrags` alongside stepped mouse dispatch so both HTML5 native drag-and-drop (`draggable="true"` with `DataTransfer` via `Input.dispatchDragEvent`) and pointer/mouse-based drag interactions work seamlessly.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `source` | string | yes | CSS selector of the element to drag |
| `target` | string | yes | CSS selector of the drop target element |
| `steps` | int | no | Number of intermediate mouse move steps (`0`–`100`, default `10`) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (default `0`, max `30000`) |

Returns: `{observed?}`.

### Cookies

#### `get_cookies`

Get browser cookies.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `urls` | []string | no | Filter cookies to these URLs. If omitted, returns cookies for the current page URL. |

Returns: `{cookies}` array of cookie objects with `{name, value, domain, path, expires?, size, http_only, secure, same_site?}`.

#### `set_cookie`

Set a browser cookie. When `domain` is omitted, falls back to the target tab's current HTTP(S) URL (returning an error if the tab is not on an HTTP/HTTPS page).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `name` | string | yes | Cookie name |
| `value` | string | yes | Cookie value |
| `domain` | string | no | Cookie domain (defaults to the target tab's current HTTP(S) URL when omitted) |
| `path` | string | no | Cookie path (default `"/"`) |
| `expires` | float | no | Cookie expiration as Unix timestamp. If omitted, creates a session cookie. |
| `http_only` | bool | no | HTTP-only flag (default `false`) |
| `secure` | bool | no | Secure flag (default `false`) |
| `same_site` | string | no | SameSite attribute: `"Strict"`, `"Lax"`, `"None"` |

#### `delete_cookies`

Delete cookies by name, domain, and/or path, or clear all browser cookies when all three (`name`, `domain`, `path`) are omitted. When `domain` is omitted alongside a `name` or `path` filter, deletion is scoped to cookies applying to the target tab's current HTTP(S) host (preserving CHIPS partition keys).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `name` | string | no | Cookie name to delete. If `name`, `domain`, and `path` are all omitted, clears all browser cookies. |
| `domain` | string | no | Scope deletion to a domain (leading `.` matches apex and subdomains) |
| `path` | string | no | Scope deletion to an exact cookie path |

### Storage

#### `get_storage`

Read one or all items from localStorage or sessionStorage.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"local"` (default) or `"session"` |
| `key` | string | no | Key to retrieve. If omitted, returns all key-value pairs. |

Returns: `{"value": "..."}` or `{"value": null}` in single-key mode (omitting `entries`), or `{"entries": {...}}` (including `{}` when empty, omitting `value`) when `key` is omitted.

#### `set_storage`

Write a key-value pair to localStorage or sessionStorage.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"local"` (default) or `"session"` |
| `key` | string | yes | Key to set |
| `value` | string | yes | Value to set |

#### `delete_storage`

Delete one or all items from localStorage or sessionStorage.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"local"` (default) or `"session"` |
| `key` | string | no | Key to delete. If omitted, clears all items. |

#### `get_storage_keys`

List all keys in localStorage or sessionStorage with value sizes. Useful for discovering stored data without retrieving potentially large values.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"local"` (default) or `"session"` |
| `limit` | int | no | Max keys to return (default all) |

Returns: `{keys}` array of `{key, size}` objects where `size` is the UTF-8 byte length of the value.

#### `get_indexeddb`

Inspect IndexedDB on the current page's origin via CDP `IndexedDB` domain (rejects opaque origins such as `about:blank`).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `database` | string | no | Database name. Omit to list all database names on the origin. |
| `store` | string | no | Object store name (requires `database`). Omit to inspect database schema (version, object stores, indexes); set to read paginated records. |
| `index` | string | no | Optional index name within `store` to read records through (requires `store`). |
| `skip` | int | no | Records to skip for pagination (default `0`, requires `store`). |
| `limit` | int | no | Max records to return (default `50`, requires `store`). |

Returns: `{databases}` in list mode; `{database, version, stores}` in inspect-database mode; `{database, store, index?, entries, has_more}` in read-records mode (values are materialized JSON).

#### `delete_indexeddb`

Delete an IndexedDB database or clear all records in one of its object stores.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `database` | string | yes | Database name |
| `store` | string | no | Object store name. If provided, clears that object store; if omitted, deletes the entire database. |

#### `get_cache_storage`

Inspect Cache Storage (`window.caches`) on the current page's origin via CDP `CacheStorage` domain (rejects opaque origins).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `cache_name` | string | no | Cache name. Omit to list caches on the origin. |
| `request_url` | string | no | Exact request URL to read a cached response body (requires `cache_name`; cannot be combined with `path_filter`/`skip`/`limit`). |
| `path_filter` | string | no | Optional case-insensitive substring filter on entry URL paths when listing entries (requires `cache_name`). |
| `skip` | int | no | Entries to skip when listing (default `0`, requires `cache_name`). |
| `limit` | int | no | Max entries to return when listing (default `50`, requires `cache_name`). |
| `range_start` | int | no | Byte offset to start slicing the response body (default `0`, requires `request_url`). |
| `range_end` | int | no | Byte offset (exclusive) to end slicing the response body (`0` = end, requires `request_url`). |

Returns: `{caches}` in list-caches mode; `{cache_name, entries, total, has_more}` in list-entries mode; `{cache_name, request_url, status, status_text?, response_type?, response_headers?, body, base64_encoded, total_bytes, truncated}` in read-response mode.

#### `delete_cache_storage`

Delete an entire Cache Storage cache or a single cached request entry.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `cache_name` | string | yes | Cache name |
| `request_url` | string | no | Exact request URL to remove from the cache. Omit to delete the entire cache. |

### Performance & Diagnostics

#### `get_performance_metrics`

Get Chrome runtime performance metrics.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{metrics}` array of `{name, value}` objects including `JSHeapUsedSize`, `JSHeapTotalSize`, `Documents`, `Nodes`, `LayoutCount`, `RecalcStyleCount`, `LayoutDuration`, `ScriptDuration`, `TaskDuration`, etc.

#### `get_layout_shifts`

Get Cumulative Layout Shift (CLS) data.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume entries) |

Returns: `{shifts, cumulative_ls}` where `shifts` is an array of `{value, had_recent_input?, sources?, timestamp}` entries and `cumulative_ls` sums shifts excluding those with `had_recent_input: true`.

#### `get_lcp`

Get Largest Contentful Paint (LCP) timeline entries captured for a tab.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume returned entries) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{entries}` array of `{render_time?, load_time?, size, url?, timestamp}` objects.

#### `get_coverage`

Get CSS and JavaScript code coverage data.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"css"`, `"js"`, or `"all"` (default `"all"`) |

Returns: `{entries}` array of per-file `{url, total_bytes, used_bytes, percentage}` objects.

### Downloads

When `--download-dir` is configured, Chrome automatically saves downloaded files. Downloads are tracked via CDP events and files are renamed from their internal GUID names to their suggested filenames on completion.

#### `get_downloads`

Get tracked file downloads with their status, progress, and file paths. Shows both completed and in-progress downloads. Downloads are browser-scoped (not per-tab).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. Defaults to active browser. |
| `mode` | string | **yes** | `"peek"` (keep completed downloads in buffer) or `"drain"` (consume them). The in-progress list is always returned by snapshot. |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{downloads, in_progress}` where each array holds `{guid, url, suggested_filename, state, received_bytes, total_bytes, path?, start_time, end_time?}` objects (`downloads` = completed/canceled entries; `in_progress` = currently downloading).

### Configuration & Emulation

#### `add_script`

Inject JavaScript to run on every new document before any page scripts. Useful for test fixtures, polyfills, disabling animations, or intercepting APIs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `source` | string | yes | JavaScript source code to evaluate on every new document |
| `evaluate_now` | bool | **yes** | If `true`, also runs the script once on the currently loaded document. If `false`, only future navigations install it. Use `evaluate` for one-shot execution on the current document only. |

Returns: `{identifier, warning?}`. `identifier` removes the new-document registration via `remove_script`; current-document side effects from `evaluate_now=true` cannot be undone via `remove_script`. `warning` is non-empty when `evaluate_now=true` was requested but the current-document evaluation failed (the new-document registration stays committed).

#### `remove_script`

Remove an injected script by its identifier.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `identifier` | string | yes | Script identifier returned by `add_script` |

#### `set_extra_headers`

Inject custom HTTP headers into all requests from a tab. Useful for auth tokens, feature flags, API keys.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `headers` | object | yes | Map of header name to value. Pass `{}` to clear. |

#### `set_permission`

Grant, deny, or reset a browser permission.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `name` | string | yes | Permission name: `geolocation`, `notifications`, `camera`, `microphone`, `clipboard-read`, `clipboard-write`, etc. |
| `setting` | string | yes | `"granted"`, `"denied"`, or `"prompt"` |
| `origin` | string | no | Scope to a specific origin. If omitted applies to all origins. |

#### `set_emulated_media`

Override CSS media type and features. Use to test dark mode, reduced motion, or print styles.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `media` | string | no | Media type: `"screen"`, `"print"`. Empty to reset. |
| `features` | array | no | Media features to override. Each entry has `name` and `value`. |

Common features: `prefers-color-scheme` (`dark`/`light`), `prefers-reduced-motion` (`reduce`/`no-preference`).

#### `set_ignore_certificate_errors`

Ignore or enforce TLS certificate errors. Enable to test against local dev servers with self-signed certificates.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ignore` | bool | yes | If true, all certificate errors will be ignored. |

### Emulation

#### `set_geolocation`

Override device geolocation. Omit all coordinate fields to reset. Requires geolocation permission to be granted via `set_permission`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `latitude` | float | no | Mock latitude (-90 to 90). Omit all fields to reset. |
| `longitude` | float | no | Mock longitude (-180 to 180). Omit all fields to reset. |
| `accuracy` | float | no | Mock accuracy in meters (default 1 when coordinates are set). |

#### `set_timezone`

Override timezone. Uses IANA timezone IDs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `timezone_id` | string | yes | IANA timezone ID (e.g. `"America/New_York"`, `"Europe/London"`, `"Asia/Tokyo"`). Empty string resets to default. |

#### `set_locale`

Override browser locale. Affects `Intl` APIs (number/date formatting).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `locale` | string | yes | ICU locale (e.g. `"en_US"`, `"fr_FR"`, `"ja_JP"`). Empty string resets to default. |

#### `set_user_agent`

Override the browser user agent string.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `user_agent` | string | yes | User agent string to use. |
| `accept_language` | string | no | Browser language to emulate (e.g. `"en-US"`, `"fr-FR"`). |
| `platform` | string | no | Platform `navigator.platform` should return (e.g. `"Win32"`, `"MacIntel"`). |

#### `set_cpu_throttling`

Throttle CPU to simulate slow devices.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `rate` | float | yes | Throttling rate (1 = no throttle, 2 = 2x slowdown, 4 = 4x slowdown). Set to 1 to disable. |

#### `set_vision_deficiency`

Simulate vision deficiencies for accessibility testing.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | yes | `"none"`, `"blurredVision"`, `"reducedContrast"`, `"achromatopsia"`, `"deuteranopia"`, `"protanopia"`, `"tritanopia"` |

#### `force_pseudo_state`

Force CSS pseudo-classes on an element via CDP `CSS.forcePseudoState` for style or screenshot inspection. Keeps the `CSS` domain enabled while any element on the tab has forced states, disables it automatically when all forced states are cleared or on main-frame cross-document navigation, and coordinates with `get_coverage` so CSS coverage still sees stylesheets and preserves active forced states afterward.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the target element |
| `states` | []string | no | Pseudo-classes to force (`"active"`, `"focus"`, `"hover"`, `"visited"`, `"focus-within"`, `"focus-visible"`, `"target"`). Omit or pass `[]` to clear forced states on this element. |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |

Returns: `{states}` array of active forced pseudo-classes applied to the element.

#### `emulate_device`

Emulate a mobile or tablet device preset (viewport, device scale factor, mobile flag, screen orientation, touch emulation, and user agent), or reset overrides back to desktop defaults.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `device` | string | yes | Preset name (`"iPhone SE"`, `"iPhone 13"`, `"iPhone 14"`, `"iPhone 14 Pro"`, `"iPhone 15"`, `"iPhone 15 Pro"`, `"iPhone 15 Pro Max"`, `"iPhone 16"`, `"iPhone 16 Pro"`, `"iPhone 16 Pro Max"`, `"iPad"`, `"iPad Mini"`, `"iPad Pro 11"`, `"Pixel 5"`, `"Galaxy S9+"`, `"Galaxy Tab S4"`), or `"reset"` |
| `landscape` | bool | no | Emulate landscape orientation (default `false`; forbidden when `device="reset"`) |

Returns: `{device, width?, height?, device_scale_factor?, mobile?, touch?, landscape?, user_agent?}`.

### Network Control

#### `emulate_network`

Emulate network conditions (offline, latency, throttled bandwidth). Call with all defaults to reset.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `offline` | bool | no | Simulate offline mode (default `false`) |
| `latency` | float | no | Minimum latency in milliseconds (0 to disable) |
| `download_throughput` | float | no | Maximum download throughput in bytes/sec (-1 = disabled) |
| `upload_throughput` | float | no | Maximum upload throughput in bytes/sec (-1 = disabled) |

#### `block_urls`

Block URLs matching patterns using CDP's `Network.setBlockedURLs` (`urlPatterns`). Accepts absolute WHATWG `URLPattern` strings (e.g. `https://example.com/*`, `*://*:*/*.css`) as well as common `*` wildcards (e.g. `*.png`, `*/api/*`, `*.example.com`, `ads.example.com/pixel.gif`), which are normalized to absolute `URLPattern` form.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `patterns` | []string | yes | Non-empty URL patterns to block. Pass empty array to clear. |

#### `add_intercept_rule`

Add a per-tab network request interception rule using CDP's `Fetch` domain (`Fetch.enable` is toggled lazily when the first rule is added and disabled when the last rule is removed or cleared). Interception applies to the tab's page target (main frame and same-process iframes, not cross-origin OOPIFs). Rules are evaluated in insertion order (first match wins); non-matching requests pass through untouched. `clear_buffers` does not clear interception rules.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `url_pattern` | string | yes | Glob pattern matching request URLs (`*` = any characters including `/`, `?` = single character, `\` = escape) |
| `method` | string | no | Optional case-insensitive HTTP method filter |
| `resource_type` | string | no | Optional case-insensitive CDP resource type filter (e.g. `XHR`, `Document`, `Script`, `Image`, `Stylesheet`; Chromium's `Fetch` domain reports `XHR` for both `fetch()` and `XMLHttpRequest`) |
| `action` | string | yes | `"fulfill"`, `"modify"`, or `"fail"` |
| `status` | int | no | HTTP status code (`100`–`599`, default `200`, `fulfill` only; bodyless statuses `1xx`, `204`, `205`, `304` forbid non-empty `body`) |
| `body` | string | no | Response body text or base64 (`fulfill` only) |
| `body_base64` | bool | no | Decode `body` from base64 (`fulfill` only) |
| `headers` | object | no | Response headers (`fulfill`), or case-insensitive request header overrides merged onto original headers (`modify`; setting `""` sends an empty header value rather than deleting the header). Header names must be valid RFC 9110 tokens without case-colliding duplicates; values must not contain CR/LF/NUL. |
| `url` | string | no | Replacement absolute URL (`modify` only) |
| `override_method` | string | no | Replacement HTTP method token (`modify` only) |
| `post_data` | string | no | Non-empty replacement request body text or base64 (`modify` only; CDP omits empty `postData`) |
| `post_data_base64` | bool | no | Decode `post_data` from base64 (`modify` only; requires non-empty payload) |
| `error_reason` | string | no | CDP `network.ErrorReason` (`fail` only, default `"Failed"`) |

Returns: `{id}`.

#### `remove_intercept_rule`

Remove an interception rule by ID. Disables CDP `Fetch` interception automatically when the last rule is removed.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `id` | string | yes | Rule ID returned by `add_intercept_rule` |

#### `list_intercept_rules`

List active interception rules on a tab in evaluation order, including match counts.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{rules}` array of rule summaries with `{id, url_pattern, method?, resource_type?, action, status?, url?, override_method?, headers?, error_reason?, matches}`.

#### `clear_intercept_rules`

Remove all interception rules on a tab and disable CDP `Fetch` interception.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{removed}`.

### Observation

Observation detects a **silent no-op**: an action that dispatches cleanly and reports success while the page does nothing. A click lands on a live element whose handler early-returns; a form submit is swallowed by a validation guard. The tool result looks identical in both cases, so without observation the caller can only find out by polling afterwards and guessing.

Observation is **not a standalone tool**. It is an `observe_window_ms` parameter on each action tool in [Interaction](#interaction), measured inline inside that tool's handler. This is the load-bearing design decision, and it is forced by latency: an LLM-driven caller has 1–10 seconds of dead time between consecutive tool calls (model thinking, token streaming, MCP transit). A separate `observe` call therefore always arrives *after* the interesting window has closed, and no amount of lookback tuning fixes it — the caller cannot know how long its own turnaround took. Observing inline sidesteps the problem: the handler timestamps the window immediately before it dispatches, so there is no gap to compensate for.

#### Window boundaries

The window **opens after the tool has waited for its target element, immediately before the dispatch**, and closes `observe_window_ms` after the dispatch returns. The two halves of that sentence are both load-bearing:

- **After the element wait.** Every selector-based action waits for its target to exist (bounded by `timeout`, 5s by default). That wait must stay *outside* the window. A page that is still bootstrapping fires requests while the element materializes, and counting them would attribute activity to an action that had not been dispatched yet — a false positive on the one signal the feature exists to provide, on precisely the slow, async pages it is for.
- **Before the dispatch.** Synchronous handler effects and microtasks fire during the dispatch itself, so a window that opened afterwards would miss them.

`window_ms` reports the true elapsed span (dispatch duration + the requested wait), not the requested wait echoed back — that is what lets a caller tell "nothing happened" apart from "I didn't wait long enough".

The DOM-mutation half of the window is anchored in **the page's own clock** (`performance.now()`, read before dispatch), not in a Go-side elapsed time. A Go-side threshold would land one CDP round trip late — the same round trip the dispatch itself costs — and would drop the synchronous mutations of single-round-trip dispatches like `select_option` and `submit_form` roughly half the time. The anchor is taken per frame, and each is pinned to that frame's execution context by CDP's *system-unique* context ID. Since those IDs are never reused, a navigation destroys the context and the stale anchor becomes unusable rather than silently wrong: the read fails instead of applying the old document's threshold to the new document's clock (which resets to zero) and sweeping up its parser-driven mutations.

`observe_window_ms` is capped at **30000**. The window is a blocking wait inside the handler, so an unbounded value would let one call pin an MCP request open indefinitely. Suggested values: **200 ms** catches synchronous handlers and microtasks; **500–1000 ms** catches most network responses.

Observation always waits its full window and never returns early on first activity. For predicate-style "block until X is true" semantics, use `wait_for`.

#### The `observed` object

```
{window_ms, network_requests, dom_mutations, url_changed, console_messages, js_errors,
 has_any_effect, truncated?, dom_mutations_unavailable?, dom_mutations_partial?}
```

Present on an action's result only when that action was called with `observe_window_ms > 0`; omitted entirely otherwise, so callers that don't want observation pay no schema cost.

`has_any_effect` is the disjunction of `network_requests > 0 || url_changed || console_messages > 0 || js_errors > 0` — the signals that are unambiguous.

**DOM mutations are intentionally excluded from that disjunction**, though the count is still reported. Real pages produce ambient `childList` mutations constantly (parser-driven on fresh navigations, framework re-renders on SPAs, async resource insertion), so folding them in would make `has_any_effect` true almost everywhere and destroy its value as a no-op signal. The caller gets `dom_mutations` as a count and compares it against expectation instead of treating any non-zero value as proof the action worked.

The MutationObserver behind that count watches `childList` (added/removed nodes) only — not `attributes` or `characterData`, which are far noisier still: focus-state changes, CSS-driven attribute toggles, and framework re-renders fire dozens of irrelevant mutations on an otherwise quiescent page.

`dom_mutations` counts **every frame we can reach**, iframes included. The observer script is injected into each document, so each frame accumulates its own buffer in its own `window`; observation evaluates in each frame's own JavaScript execution context and sums. Reading only the top frame would report a confident `0` for an action whose entire effect landed inside an iframe.

Some frames cannot be reached. A **cross-origin iframe is out-of-process**: its JavaScript runs on a separate CDP session that this server is not attached to, so its mutation buffer is simply unreachable. That covers most third-party ads, embeds, and payment frames. We can still *see that such a frame exists* — it announces itself with a `frameAttached` before it swaps out of our process (`Page.getFrameTree` does **not** list it afterwards) — so rather than pretend its mutations are zero, the count is flagged as a lower bound via `dom_mutations_partial`. The same flag covers a frame born during the window (never anchored) and a subframe replaced mid-window (its buffer died with it).

Only the **top** frame being replaced makes the whole count meaningless, since that means the page itself is gone; that is `dom_mutations_unavailable`. A replaced *subframe* is not that: the page is intact and every other frame still counts honestly, so the count stands and is marked partial.

Network counts include requests that are still **in flight**. A request that starts inside the window but completes after it closes is counted, because the alternative — counting only requests the collector has seen finish — would report zero for exactly the case observation exists to catch: an action that fires a slow XHR.

Observation never turns a successful action into an error: the action has already succeeded by the time the window closes, and failing the call because a *measurement* came back partial would hand the caller an error it cannot act on. Partial measurements are reported instead, via three flags:

| Flag | Meaning |
|------|---------|
| `truncated` | The window was cut short: the tab closed, or the client disconnected, before it elapsed. (A navigation does *not* truncate — it leaves the tab's context live.) The counts are partial, and **a false `has_any_effect` is not evidence the action did nothing**. |
| `dom_mutations_unavailable` | The mutation count could not be attributed to the action, because the **top-frame** document was replaced during the window, a JavaScript modal dialog is open (blocking renderer evaluation; observation short-circuits immediately via the tab's Dialog state), or the page could not be read. `dom_mutations` is 0 and carries no information; use `url_changed` or `get_dialog` instead. |
| `dom_mutations_partial` | `dom_mutations` is a **lower bound**: some frame's mutations could not be seen — a cross-origin (out-of-process) iframe, a frame created during the window, or a subframe replaced mid-window. The count is honest for the frames we could see. A zero here does **not** mean the DOM did not change. |

All are omitted when false. `dom_mutations_unavailable` exists because the mutation buffer is per-document: when the page navigates, the buffer the window anchored against is gone, and the new document's parser-driven mutations are not the action's doing.

## Internal Architecture

### Package Structure

```
chromedp-mcp/
  cmd/
    chromedp-mcp/
      main.go           # Entry point, CLI flags, signal handling, MCP server setup, stdio transport
  internal/
    id/
      id.go             # Short random identifier generation for browsers, tabs, and rules
    browser/
      browser.go        # Browser type: launch/connect lifecycle, target adoption, download collector
      manager.go        # BrowserManager: registry of browsers, active browser tracking
    tab/
      tab.go            # Tab type: wraps a chromedp target context, owns collectors and per-tab operation locks
      manager.go        # TabManager: per-browser tab registry, MRU active tab tracking
    profile/
      profile.go        # Chrome profile discovery from Local State, platform-specific user data dir detection
    collector/
      collector.go      # Generic RingBuffer[T] with drain/peek/filter
      console.go        # Console log collector (runtime.EventConsoleAPICalled)
      dialog.go         # Active JavaScript modal dialog state per tab
      download.go       # Browser-scoped download tracking and async GUID-to-filename renaming
      errors.go         # JS error collector (runtime.EventExceptionThrown)
      execcontext.go    # Per-frame main-world JS execution contexts and frame hierarchy (observation & frame targeting)
      intercept.go      # Ordered per-tab network request interception rules and glob/header helpers
      network.go        # Network request/response collector (request lifecycle events)
      performance.go    # Performance timeline collector (LCP, layout shifts)
      time.go           # CDP timestamp conversion helpers
      urlchange.go      # Main-frame navigation and SPA URL-change collector (activity observation)
      websocket.go      # WebSocket connection and per-direction frame ring collector
    tools/
      tools.go          # Tool registration, shared input/output types, schema enum helpers, annotations
      browser.go        # browser_launch, browser_connect, browser_activate, browser_close, browser_list
      profile.go        # browser_list_profiles
      tabs.go           # tab_new, tab_list, tab_activate, tab_close
      navigation.go     # navigate, reload, go_back, go_forward, wait_for
      visual.go         # screenshot, pdf, set_viewport
      dom.go            # query, get_html, get_text, get_accessibility_tree
      frames.go         # list_frames and same-origin iframe evaluation/wait helpers
      console.go        # get_console_logs, get_js_errors, clear_console
      buffers.go        # clear_buffers
      network.go        # get_network_requests, get_response_body, get_request_body, get_websocket_frames
      intercept.go      # add_intercept_rule, remove_intercept_rule, list_intercept_rules, clear_intercept_rules
      js.go             # evaluate (with optional selector)
      interaction.go    # click, type, select_option, submit_form, scroll, hover, focus, press_key, upload_files, get_dialog, handle_dialog, drag_and_drop
      observation.go    # Inline activity observation window helpers (DOM mutations, network, URL, console, errors)
      cookies.go        # get_cookies, set_cookie, delete_cookies
      storage.go        # get_storage, set_storage, delete_storage, get_storage_keys
      indexeddb.go      # get_indexeddb, delete_indexeddb
      cachestorage.go   # get_cache_storage, delete_cache_storage
      performance.go    # get_performance_metrics, get_layout_shifts, get_lcp, get_coverage
      downloads.go      # get_downloads
      config.go         # add_script, remove_script, set_extra_headers, set_permission, set_emulated_media, set_ignore_certificate_errors
      emulation.go      # set_geolocation, set_timezone, set_locale, set_user_agent, set_cpu_throttling, set_vision_deficiency, force_pseudo_state, emulate_device, emulate_network, block_urls
```

### Key Components

- **`browser.Browser`** — Manages a single Chrome instance (in `ModeLaunch` or `ModeConnect`), its allocator and browser CDP contexts, its `tab.Manager`, and its browser-scoped `collector.Download`.
- **`browser.Manager`** — Thread-safe (`sync.Mutex`) registry of managed browsers with MRU active-browser fallback and dead-browser pruning on access.
- **`tab.Tab`** — Wraps a `chromedp` target `context.Context`, owns the per-tab event collectors (`Console`, `JSErrors`, `Network`, `WebSocket`, `Performance`, `URLChanges`, `ExecContexts`, `Dialog`, `Intercept`), and provides per-tab operation locks (`interceptOpMu`, `cssOpMu`/`cssStateMu`, `dragOpMu`) for stateful multi-step CDP flows.
- **`tab.Manager`** — Thread-safe (`sync.RWMutex`) per-browser tab registry with MRU active-tab tracking and external target-close pruning.
- **`collector.RingBuffer[T]`** — Generic bounded mutex-protected buffer supporting filtered `Drain` (removing only returned matches within `limit`), `Peek`, and `Clear`.

### Lifecycle

1. **Startup**: Create `browser.Manager` (empty — no browser yet). Create MCP `Server`. Register all tools. Run `server.Run(ctx, StdioTransport)`.
2. **First tool call**: If no browser exists, the tool returns an error telling the LLM to call `browser_launch` or `browser_connect`. If a browser has zero open tabs, tab-scoped tools return an error telling the LLM to call `tab_new`.
3. **Browser creation**: `browser_launch` calls `chromedp.NewExecAllocator` + `chromedp.NewContext` and starts Chrome at the browser CDP level via `chromedp.Targets`, attaching Chrome's initial default page target(s) into `tab.Manager` (falling back to `NewTab` if none attach). `browser_connect` resolves `/json/version` when needed, calls `remote.NewAllocator` + `chromedp.NewContext`, queries `chromedp.Targets`, and adopts existing open page targets into `tab.Manager` without spawning a blank tab.
4. **Tab creation**: `tab.Manager.NewTab()` calls `chromedp.NewContext(browserCtx, chromedp.WithListener(...))`, creates collectors, attaches CDP event listeners before target startup, and returns the tab ID.
5. **Tool execution**: Tool handler resolves the target tab (explicit ID → active tab, erroring if none exist), executes chromedp actions on the tab's context, reads from collectors as needed, returns the result.
6. **Shutdown**: Server context is cancelled. Attached tabs detach cleanly (leaving user tabs open in connected browsers) while server-created tabs close; all browser contexts are cancelled (killing launched Chromes, disconnecting from connected ones).

### Error Handling

- No browser exists: return tool error instructing the caller to call `browser_launch` or `browser_connect`.
- Invalid browser/tab ID: return tool error stating the ID was not found.
- Selector not found: return tool error with the selector.
- Navigation timeout: return tool error with the URL and timeout duration.
- Chrome crash: detect via context cancellation, return tool error, and prune/close dead browsers on subsequent access.
- All errors are returned as MCP tool errors (`IsError: true`), not protocol errors, so the LLM can reason about them and retry.

### Concurrency

- Each tab has its own `chromedp` target `context.Context` and collectors. Tools targeting different tabs can run concurrently.
- Collectors use `sync.Mutex` for thread-safe access.
- `browser.Manager` uses `sync.Mutex` (since reads also prune dead browsers), and `tab.Manager` uses `sync.RWMutex` for concurrent lookups with exclusive creation/deletion.
- `chromedp` multiplexes concurrent CDP commands on a single target, so stateful multi-step per-tab sequences (Fetch interception lifecycle, CSS forced pseudo-states and coverage, and `drag_and_drop` interception) are explicitly serialized via per-tab mutexes on `tab.Tab`.

## Tool Annotations

All tools include explicit MCP `ToolAnnotations` (including non-nil `destructiveHint`) for client-side behavior hints:

| Tool Category | `readOnlyHint` | `destructiveHint` | `idempotentHint` |
|---------------|----------------|-------------------|------------------|
| Browser (`browser_launch`, `browser_connect`) | false | false | false |
| Browser (`browser_activate`) | false | false | true |
| Browser (`browser_list`, `browser_list_profiles`) | true | false | true |
| Browser (`browser_close`) | false | true | false |
| Tab (`tab_new`) | false | false | false |
| Tab (`tab_list`) | true | false | true |
| Tab (`tab_activate`) | false | false | true |
| Tab (`tab_close`) | false | true | false |
| Navigation (`navigate`, `reload`, `go_back`, `go_forward`) | false | false | false |
| Waiting (`wait_for`) | true | false | true |
| Visual (`screenshot`, `pdf`) — without `--download-dir` | true | false | true |
| Visual (`screenshot`, `pdf`) — with `--download-dir` | false | false | true |
| Visual (`set_viewport`) | false | false | true |
| DOM Inspection (`query`, `get_html`, `get_text`, `get_accessibility_tree`, `list_frames`) | true | false | true |
| Console/Errors (`get_console_logs`, `get_js_errors`) | false | false | false |
| Console/Buffers (`clear_console`, `clear_buffers`) | false | false | true |
| Network (`get_network_requests`, `get_websocket_frames`) | false | false | false |
| Network (`get_response_body`, `get_request_body`, `list_intercept_rules`) | true | false | true |
| Network (`add_intercept_rule`) | false | false | false |
| Network (`remove_intercept_rule`) | false | true | false |
| Network (`emulate_network`, `block_urls`, `clear_intercept_rules`) | false | false | true |
| Downloads (`get_downloads`) | false | false | false |
| JS Execution (`evaluate`) | false | false | false |
| Interaction (`get_dialog`) | true | false | true |
| Interaction (`click`, `type`, `select_option`, `submit_form`, `scroll`, `hover`, `focus`, `press_key`, `upload_files`, `handle_dialog`, `drag_and_drop`) | false | false | false |
| Cookies (`get_cookies`) | true | false | true |
| Cookies (`set_cookie`) | false | false | true |
| Cookies (`delete_cookies`) | false | true | false |
| Storage (`get_storage`, `get_storage_keys`, `get_indexeddb`, `get_cache_storage`) | true | false | true |
| Storage (`set_storage`) | false | false | true |
| Storage (`delete_storage`, `delete_indexeddb`, `delete_cache_storage`) | false | true | false |
| Performance (`get_performance_metrics`, `get_coverage`) | true | false | true |
| Performance (`get_layout_shifts`, `get_lcp`) | false | false | false |
| Config (`add_script`) | false | false | false |
| Config (`remove_script`) | false | true | false |
| Config (`set_extra_headers`, `set_permission`, `set_emulated_media`, `set_ignore_certificate_errors`) | false | false | true |
| Emulation (`set_geolocation`, `set_timezone`, `set_locale`, `set_user_agent`, `set_cpu_throttling`, `set_vision_deficiency`, `force_pseudo_state`, `emulate_device`) | false | false | true |

## Configuration

All browser configuration is done via tools at runtime. The server accepts optional flags:

```
chromedp-mcp [--download-dir <path>] [--allowed-profiles <names>] [--user-data-dir <path>]
```

| Flag | Description |
|------|-------------|
| `--download-dir` | Directory for saving screenshots, PDFs, and downloads (supports `~` expansion). When set, `screenshot` and `pdf` tools accept a `filename` parameter to save output to disk, and Chrome downloads are enabled with automatic file saving and event tracking via `get_downloads`. Path traversal is blocked — filenames must not contain directory separators. The directory is created automatically if it doesn't exist. |
| `--allowed-profiles` | Comma-separated list of Chrome profile display names the LLM may use (e.g. `"Work,Personal"`). When set, registers the `browser_list_profiles` tool and enables the `profile` parameter on `browser_launch`. Profiles not in this list are never exposed to the LLM. The Chrome/Chromium user data directory is auto-detected from platform defaults unless `--user-data-dir` is specified. |
| `--user-data-dir` | Override the Chrome/Chromium user data directory used for profile discovery. When omitted, the default platform location is auto-detected (e.g. `~/.config/google-chrome` on Linux, `~/Library/Application Support/Google/Chrome` on macOS). Only relevant when `--allowed-profiles` is set. Supports `~` expansion. |

When `--download-dir` is not set, the tools return binary data inline only and requesting a `filename` returns an error.

### User Profile Security

> **Warning:** Enabling `--allowed-profiles` gives the LLM access to real Chrome profiles, including saved cookies, active sessions, localStorage, and extension data. The LLM can act as the user on any site they are logged into in that profile. Only expose profiles you understand the implications of, and never expose profiles containing sensitive credentials to untrusted LLM providers.

Profile access is disabled by default. The `--allowed-profiles` flag is the explicit opt-in gate. When not set, the `browser_list_profiles` tool is not registered and the `profile` parameter on `browser_launch` is rejected. Each launched browser gets a fresh temporary profile that is discarded on close.

Profile discovery works by reading Chrome's `Local State` JSON file, which maps profile directory names (`Default`, `Profile 1`, `Profile 2`, etc.) to user-chosen display names. The `--allowed-profiles` flag accepts display names, and only matching profiles are returned by `browser_list_profiles` or accepted by `browser_launch`. The `--user-data-dir` flag overrides the auto-detected user data directory, which is useful for pointing at a different Chrome/Chromium installation.

Note that Chrome enforces a singleton lock on user data directories. Only one Chrome process can use a given user data directory at a time. If the user's Chrome is already running, launching a browser with a profile from the same user data directory will fail.

## Future Features

Features not in the initial scope but supported by CDP and worth adding later.

### Network Control

- **Certificate Inspection** — Get site certificate details via `network.GetCertificate`.

### Security & Auth

- **WebAuthn** — Create virtual authenticators, add/manage credentials, simulate user verification via `webauthn` domain. Enables testing passkey and FIDO2 flows without physical hardware.

### Media

- **Media Inspection** — Monitor audio/video playback, codecs, errors, and buffering via `media` domain. Useful for debugging streaming and video player issues.
- **Web Audio** — Inspect Web Audio graph and real-time processing data via `webaudio` domain.

### Debugging

- **CSS Manipulation** — Add CSS rules and modify stylesheets via `css` domain (`force_pseudo_state` is already implemented).
- **DOM Manipulation** — Modify the DOM tree directly (set attributes, set outer HTML, remove nodes) via `dom` domain.
- **DOM Snapshots** — Capture full DOM snapshots including styles and layout via `domsnapshot` domain. Useful for visual regression testing.
- **MHTML Snapshots** — Capture a complete page as a single MHTML file via `page.CaptureSnapshot`.

### Service Workers & PWA

- **Service Worker Management** — List, start, stop, unregister service workers. Simulate push messages and sync events via `serviceworker` domain.
- **PWA Inspection** — Inspect Progressive Web App manifest and installation state via `pwa` domain.

### Profiling

- **JavaScript CPU Profile** — Start/stop CPU profiling and analyze time spent in functions via `profiler` domain.
- **Heap Snapshot** — Take heap snapshots for memory leak analysis via `heapprofiler` domain.
- **Tracing** — Record Chrome trace events (categories: rendering, scripting, painting, loading) via `tracing` domain. Provides detailed performance flame charts.

### Browser

- **Window Management** — Get/set browser window position and size via `browser.GetWindowBounds` / `browser.SetWindowBounds`.

### Other

- **Touch Gestures** — Simulate multi-touch, pinch, and swipe gestures via `input.DispatchTouchEvent` and `input.SynthesizePinchGesture` / `SynthesizeScrollGesture`.
- **Animations** — Inspect and control CSS animations via `animation` domain. Pause, seek, change playback rate.
- **Layer Tree** — Inspect compositing layers for debugging rendering performance and GPU usage via `layertree` domain.
