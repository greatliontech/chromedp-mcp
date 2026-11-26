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
                                      |  - collectors  |
                                      +----------------+
```

The server exposes 41 MCP tools by default (plus 2 conditionally registered tools: `get_downloads` when `--download-dir` is configured and `browser_list_profiles` when `--allowed-profiles` is configured; 43 max). Under the hood, `chromedp` drives Chrome via the Chrome DevTools Protocol. The server captures console logs, network traffic, WebSocket frames, JS errors, dialog state, and performance timeline data in background collectors, making them available to the LLM on demand.

## Transport

stdio only. This is the standard for CLI-integrated MCP servers (Claude Desktop, Claude Code, OpenCode, etc.).

## Browser Lifecycle

The browser lifecycle is entirely tool-driven. The LLM decides when to launch or connect to a browser, and with what settings. The only CLI-level configuration flags are `--download-dir` (enables disk saving and downloads), `--allowed-profiles` (gates access to real Chrome user profiles), and `--user-data-dir` (overrides where profiles are discovered). See [Configuration](#configuration) and [User Profile Security](#user-profile-security).

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
| `width` | int | no | Initial viewport width in pixels (default `1920`) |
| `height` | int | no | Initial viewport height in pixels (default `1080`) |
| `profile` | string | no | Chrome profile display name to use (e.g. `"Work"`). Only exposed in `InputSchema` when `--allowed-profiles` is configured; rejected if passed otherwise. |

Returns: `{browser_id, tabs}` where `tabs` is the array of attached initial tabs (`{id, url, title, active}`).

When `profile` is specified, the browser is launched with `--user-data-dir` and `--profile-directory` pointing to the real Chrome profile. When omitted, a temporary ephemeral profile is used. Closing the server kills any launched browsers.

#### `browser_list_profiles`

List available Chrome/Chromium user profiles that can be used with `browser_launch`. Registered only when `--allowed-profiles` is configured. Returns only profiles in the allowed list.

No parameters.

Returns: `{profiles}` array of `{name, dir}` objects (always `[]` when none match).

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

List all live managed browsers (pruning any that exited externally).

No parameters.

Returns: `{browsers}` array of `{id, mode, active, tabs}` objects (where `tabs` is the open tab count).

### Active Browser

When multiple browsers are managed, one is the **active browser** tracked in MRU order. Tab tools that omit `browser` operate on the active browser (or, when an explicit `tab` ID is given, resolve the tab across all managed browsers). Closing the active browser promotes the most recently used remaining browser.

## Tab Model

The server supports multiple concurrent tabs per browser. Each tab is identified by a string **tab ID** returned on creation. Most tools accept an optional `tab` parameter; when omitted, the tool operates on the **active tab** of the active browser.

Internally, each tab maps to a `chromedp` target `context.Context` derived from the browser context. Creating a tab creates a new Chrome target; closing a tab cancels its context.

**Automatic popup adoption:** Each browser enables `Target.setDiscoverTargets` and listens for `Target.targetCreated` events. When a page opens a popup or `target="_blank"` tab (`OpenerID != ""`), the server automatically attaches it in the background (`AttachBackgroundTarget`) so it appears immediately in `tab_list` without stealing the caller's active tab.

### Tab Management Tools

#### `tab_new`

Create a new tab, optionally navigating to a URL.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | no | URL to navigate to |
| `browser` | string | no | Browser ID. Defaults to active browser. |

Returns: `{tab_id, url?}`. The new tab becomes the active tab. If navigation fails, the newly created tab is closed cleanly rather than orphaned.

#### `tab_list`

List all open tabs with their IDs, URLs, and titles (via non-blocking `Target.getTargetInfo`, so open JS dialogs do not hang listing).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. Defaults to active browser. |

Returns: `{tabs}` array of `{id, url, title, active}` objects.

#### `tab_activate`

Set a tab as the active tab (updating its owning browser to active as well) and bring the tab to the front via `Page.bringToFront`.

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

No implicit auto-creation. If no browser exists when a tool that requires one is called, an error instructs the caller to call `browser_launch` or `browser_connect`. `browser_launch` attaches Chrome's initial default tab (and `browser_connect` adopts existing open tabs) so tab-scoped tools work immediately; if all tabs are later closed (or a connected browser has zero open tabs), calling a tab tool returns an error instructing the caller to call `tab_new`.

## Element Targeting, Snapshot Refs, Shadow DOM & Iframes

Tools that operate on elements share a unified target resolver (`internal/tools/target.go`) supporting three addressing modes:

1. **Snapshot refs (`ref`)**: Short handles (`e1`, `e2`, ...) assigned by `snapshot`. Each `tab.SnapshotRef` records `(BackendNodeID, FrameID, Role, Name)`, while per-tab generation counters (`snapshotGen` and `snapshotFrameGen`) clear or prune refs automatically on main-frame navigation, subframe navigation/detach, or target crash with a stale-ref error directing the caller to re-snapshot. Refs pierce open Shadow DOM transparently and target elements in same-origin iframes directly (when snapshotted with `frame`). `ref` is mutually exclusive with `selector`, `frame`, and `(x, y)`.
2. **CSS and Shadow DOM selectors (`selector`, optional `frame`)**: Standard CSS selectors plus `>>>` combinators to pierce nested open shadow roots (e.g. `my-app >>> my-card >>> button.save`). Quoted or escaped `>>>` inside attribute selectors is preserved. Passing a reachable `frame` ID from `list_frames` scopes selector lookup and evaluation to that same-origin iframe; cross-origin OOPIFs return an explicit out-of-process error.
3. **Viewport coordinates (`x`, `y`)**: Supported on pointer tools (`click`, `hover`, and `source_x`/`source_y`/`target_x`/`target_y` on `drag_and_drop`) for canvas/SVG/custom hit targets. Both `x` and `y` must be finite numbers and are mutually exclusive with `ref`/`selector`/`frame`.

## Event Collectors

The server registers CDP event listeners per tab that buffer events automatically from tab startup.

### Collected Event Types

| Collector | CDP Events | Buffer Behavior |
|-----------|------------|-----------------|
| **Console** | `runtime.EventConsoleAPICalled` | Ring buffer (default 1000 entries). Normalizes CDP `"warning"` to `"warn"` and captures top stack frame `source` (`url:line:column`). |
| **JS Errors** | `runtime.EventExceptionThrown` | Ring buffer (default 500 entries). Preserves primitive throws (`throw "boom"` / `Promise.reject("x")`). |
| **Network** | `network.EventRequestWillBeSent`, `network.EventResponseReceived`, `network.EventLoadingFinished`, `network.EventLoadingFailed` | Ring buffer of request/response pairs (default 1000 entries); preserves redirect chains, multi-value headers (joined with `\n`), failure details (`blocked_reason`, `cors_error`, `canceled`), and timing breakdown (`transfer_ms`, etc.). Bodies captured lazily on demand via `get_network_body`. |
| **WebSocket** | `network.EventWebSocketCreated`, `network.EventWebSocketWillSendHandshakeRequest`, `network.EventWebSocketHandshakeResponseReceived`, `network.EventWebSocketFrameSent`, `network.EventWebSocketFrameReceived`, `network.EventWebSocketFrameError`, `network.EventWebSocketClosed` | Tracks up to 100 connections per tab with bounded sent/received frame ring buffers (500 frames per direction per socket). |
| **Performance Timeline** | `performancetimeline.EventTimelineEventAdded` | Ring buffers for layout shifts (default 200 entries; computes W3C session-window CLS excluding `had_recent_input` shifts) and LCP (default 50 entries). |
| **URL Changes** | `page.EventFrameNavigated`, `page.EventNavigatedWithinDocument` | Ring buffer (default 100 entries) used by inline activity observation. |
| **Dialog** | `page.EventJavascriptDialogOpening`, `page.EventJavascriptDialogClosed` (plus crash/navigation/frame-detach clearing) | Tracks the single active JS modal dialog per tab and broadcasts opening notifications (`OpeningNotify()`) so in-flight actions interrupt cleanly instead of hanging. |
| **Downloads** | `browser.EventDownloadWillBegin`, `browser.EventDownloadProgress` | Browser-scoped ring buffer (default 200 completed/canceled entries) plus live in-progress tracking and async GUID-to-filename renaming without overwrites. |

Buffer-reading tools support two read modes (`"peek"` and `"drain"`):
- **`mode: "drain"`**: Return and remove only the entries that match the filter (and are within `limit`). Non-matching entries and matches beyond `limit` are retained so successive calls paginate.
- **`mode: "peek"`**: Return matching entries without modifying the buffer.

`mode` is required on `get_console_logs`, `get_js_errors`, `get_network_requests`, `get_websocket_frames`, and `get_downloads` (and defaults to `"peek"` on `get_performance` because runtime metrics are always live). WebSocket connections in `get_network_requests` are long-lived and always returned by snapshot regardless of `mode`.

## Tools

All tools return structured output with explicit `OutputSchema` via `CallToolResult`. Tools that produce images return `ImageContent` (PNG/JPEG); `pdf` returns `EmbeddedResource` (`application/pdf`) or a saved file path.

Every tab-scoped tool accepts an optional `tab` parameter (string tab ID); when omitted, the active tab of the active browser is used.

### Navigation

#### `navigate`

Navigate a tab: go to a URL, reload, or move through session history. Returns final URL and title (evaluated via JS with non-blocking `Tab.Info()` fallback when a JS dialog interrupts or the context is stale) plus HTTP `status` from CDP network responses on `goto`/`reload`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | no | `"goto"` (default), `"reload"`, `"back"`, or `"forward"` |
| `url` | string | no | URL to navigate to (required for `action="goto"`; forbidden for other actions) |
| `wait_until` | string | no | For `action="goto"`: `"load"` (default), `"domcontentloaded"`, or `"networkidle"` (Chrome's built-in `networkIdle` lifecycle event: 0 in-flight requests for 500ms); forbidden for other actions |
| `bypass_cache` | bool | no | For `action="reload"`: bypass browser cache (default `false`; forbidden for other actions) |

Returns: `{url, title, status?}`.

#### `wait_for`

Wait for a single condition to be met. Provide exactly one of `selector`, `text`, `url`, or `expression`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS/`>>>` shadow selector to wait for |
| `state` | string | no | For `selector`: `"visible"` (default), `"hidden"`, `"attached"`, or `"detached"` (only valid with `selector`) |
| `text` | string | no | Wait until page/frame visible text contains this substring |
| `url` | string | no | Wait until tab URL contains this substring (cannot combine with `frame`) |
| `expression` | string | no | JS expression to poll until truthy (supports Promises) |
| `frame` | string | no | Optional same-origin frame ID from `list_frames` (valid with `selector`, `text`, or `expression`) |
| `timeout` | int | no | Timeout in milliseconds (default `30000`) |

Expressions are wrapped in `Promise.resolve(\nEXPR\n).then(Boolean)` server-side before polling so non-primitive DOM returns coerce cleanly, Promises are awaited, and trailing line comments do not break the wrapper.

### Visual Feedback

#### `screenshot`

Capture a screenshot of the viewport, full page, or a specific element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref of an element to screenshot (mutually exclusive with `selector`/`frame`) |
| `selector` | string | no | CSS/`>>>` shadow selector of an element to screenshot. If both `ref` and `selector` are omitted, captures the viewport. |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`); encoding runs outside the selector timeout |
| `full_page` | bool | no | Capture the full scrollable page (default `false`; ignored if `ref` or `selector` is set) |
| `format` | string | no | `"png"` (default) or `"jpeg"` |
| `quality` | int | no | JPEG quality `1`–`100` (default `80`; ignored for PNG) |
| `max_dimension` | int | no | Maximum allowed width or height in physical pixels (accounting for DPR); downscales proportionally via CDP clip scaling |
| `filename` | string | no | Save atomically (`0o600` temp file + rename) to `--download-dir`. Omit to return inline only; image is still returned inline. |

Returns: `ImageContent` (plus `TextContent` with the saved path when `filename` is set).

#### `pdf`

Generate a PDF of the current page.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `landscape` | bool | no | Landscape orientation (default `false`) |
| `print_background` | bool | no | Include background graphics (default `true`) |
| `scale` | float | no | Page rendering scale (default `1.0`) |
| `paper_width` | float | no | Paper width in inches (default `8.5`) |
| `paper_height` | float | no | Paper height in inches (default `11`) |
| `page_ranges` | string | no | Page ranges, e.g. `"1-5, 8"` (default all pages) |
| `filename` | string | no | Save atomically (`0o600` temp file + rename) to `--download-dir`. When set, returns a `TextContent` path instead of the inline blob. |

Returns: `EmbeddedResource` (`application/pdf`), or `TextContent` path when `filename` is set.

### DOM & Accessibility Inspection

#### `snapshot`

Capture a compact indented accessibility snapshot of the page (or a same-origin frame/subtree) with short element refs (`[ref=e1]`, `[ref=e2]`, ...) usable across interaction, visual, DOM, JS, and pseudo-state tools. Collapses ignored/unnamed generic wrappers and redundant `StaticText` children, pierces open Shadow DOM, and replaces the tab's active ref table on each call. Fails fast if a JavaScript modal dialog is open.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional same-origin frame ID from `list_frames` to snapshot instead of the main frame |
| `selector` | string | no | Optional CSS/`>>>` shadow selector to scope the snapshot to a subtree |
| `depth` | int | no | Max semantic tree depth (`0` = unlimited) |
| `interactive_only` | bool | no | Include only interactive/focusable controls (buttons, links, inputs, etc.) |
| `max_bytes` | int | no | Max UTF-8 bytes for the rendered snapshot text (default `50000`) |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |

Returns: `{url, title, snapshot, ref_count, truncated?, total_bytes?}`.

#### `query`

Query the DOM with a CSS or `>>>` Shadow DOM selector and return matching elements.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `frame` | string | no | Optional same-origin frame ID from `list_frames` |
| `selector` | string | yes | CSS/`>>>` shadow selector |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `limit` | int | no | Max elements to return (default `10`) |
| `attributes` | bool | no | Include element attributes (default `true`) |
| `computed_style` | []string | no | CSS property names to include computed values for |
| `outer_html` | bool | no | Include outer HTML (default `false`) |
| `text` | bool | no | Include text content (default `true`) |
| `bbox` | bool | no | Include bounding box dimensions relative to the targeted frame's viewport (default `false`) |

Returns: `{elements, total}` where `elements` is an array of `{index, tag_name, attributes?, text?, outer_html?, computed_style?, bbox?}` objects.

#### `get_dom`

Get text or HTML content of the page, a frame, or a specific element, bounded by `max_bytes` on valid UTF-8 rune boundaries.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `format` | string | yes | `"text"` (visible/DOM text) or `"html"` (markup) |
| `ref` | string | no | Snapshot ref (mutually exclusive with `selector`/`frame`) |
| `frame` | string | no | Optional same-origin frame ID from `list_frames` |
| `selector` | string | no | CSS/`>>>` shadow selector (defaults to `"body"` for `text` or `"html"` for `html`) |
| `outer` | bool | no | For `format="html"`: return outer HTML (default `true`) or inner HTML (`false`) |
| `hidden` | bool | no | For `format="text"`: include hidden text via `textContent` instead of visible `innerText` (default `false`) |
| `max_bytes` | int | no | Max UTF-8 bytes to return (default `65536`) |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |

Returns: `{content, truncated?, total_bytes?}`.

#### `list_frames`

List all frames in a tab, including the main frame, same-origin in-process iframes (`reachable: true`), and known out-of-process cross-origin iframes (`reachable: false`). Same-origin frame IDs can be passed as `frame` on `snapshot`, `query`, `get_dom`, `wait_for`, `screenshot`, `evaluate`, `emulate(action="pseudo_state")`, and interaction tools; cross-origin OOPIFs return a clear error. Fails fast if a JavaScript modal dialog is open. Note: because `Page.getFrameTree` omits OOPIFs and Chrome does not emit `frameDetached(remove)` on this session when an already-swapped OOPIF element is removed directly from the top frame, pre-existing OOPIFs on `browser_connect`-adopted tabs are not visible until navigation, and a directly removed top-level OOPIF persists in `list_frames` until top-frame navigation (descendant OOPIFs of a detached/navigated/swapped subframe are pruned immediately).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: `{frames}` array of `{id, parent_id?, name?, url?, security_origin?, is_main, reachable}` objects.

### Console & Buffers

#### `get_console_logs`

Get captured console messages.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `level` | string | no | Open string filter matching any CDP console level (e.g. `"log"`, `"warn"`, `"error"`, `"info"`, `"debug"`, `"assert"`, `"trace"`). CDP `"warning"` is normalized to `"warn"`. |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume matching entries) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{logs}` array of `{level, text, source?, timestamp}` objects.

#### `get_js_errors`

Get captured JavaScript exceptions and unhandled promise rejections.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries) or `"drain"` (consume entries) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `{errors}` array of `{message, source?, line, column, stack_trace?, timestamp}` objects.

#### `clear_buffers`

Clear one or more event collector buffers while preserving live/in-flight state: open WebSockets stay tracked (clearing their frame rings and counts and removing closed connections), in-progress downloads and in-flight HTTP requests keep tracking, the URL-change baseline is preserved, and live modal dialog state is untouched. Clearing only `["downloads"]` without `tab` succeeds even with zero open tabs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID. When provided without `browser`, `downloads` resolves to that tab's owning browser. |
| `browser` | string | no | Browser ID (defaults to active browser). When provided without `tab`, tab-scoped targets clear that browser's active tab. When both are provided, `tab` ownership is validated. |
| `targets` | []string | no | Subset of `"console"`, `"errors"`, `"network"`, `"websocket"`, `"performance"`, `"url_changes"`, `"downloads"`, `"all"`. Omit or pass `["all"]` to clear all buffers; explicit `[]` is rejected. |

Returns: `{cleared}` listing the cleared buffer names in canonical order.

### Network Inspection & Control

#### `get_network_requests`

Get captured network requests (HTTP and WebSocket connections).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | **yes** | `"peek"` (keep entries in HTTP buffer) or `"drain"` (consume matching entries within `limit`). WebSocket connections are long-lived and always returned by snapshot. |
| `limit` | int | no | Max entries to return (default all) |
| `type` | string | no | CDP resource type filter: `"document"`, `"stylesheet"`, `"image"`, `"media"`, `"font"`, `"script"`, `"texttrack"`, `"xhr"`, `"fetch"`, `"prefetch"`, `"eventsource"`, `"websocket"`, `"manifest"`, `"signedexchange"`, `"ping"`, `"cspviolationreport"`, `"preflight"`, `"fedcm"`, `"emailverification"`, `"other"` |
| `status_min` | int | no | Filter by minimum HTTP status code |
| `status_max` | int | no | Filter by maximum HTTP status code |
| `url_pattern` | string | no | Filter by URL substring match |
| `failed_only` | bool | no | Return only failed requests (default `false`) |
| `fields` | []string | no | Project each entry to a subset of JSON field names (enum of all 22 `NetworkEntry` JSON keys, e.g. `["id","method","url","status"]`). Unknown field names produce an error before draining. |

Returns: `{requests}` array of `{id, url, method, status?, type?, timing?, request_headers?, response_headers?, size?, error?, blocked_reason?, cors_error?, canceled?, failed?, start_time, end_time?, has_request_body?, request_body?, request_body_base64?, request_body_truncated?, frames_sent_count?, frames_received_count?}`.

#### `get_network_body`

Get the response or request body of a specific network request with optional byte-range slicing.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | Request ID from `get_network_requests` |
| `part` | string | no | `"response"` (default) or `"request"` |
| `range_start` | int | no | Inclusive byte offset to start from (default `0`, must be `>= 0`) |
| `range_end` | int | no | Exclusive byte offset to stop at (`0` = end of body, must be `0` or `>= range_start`). Clamped past end; when `base64_encoded=true`, offsets address the decoded binary. |

Returns: `{body, base64_encoded, total_bytes, truncated?}`.

#### `get_websocket_frames`

Get WebSocket frames sent and/or received on a specific connection.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | WebSocket connection request ID from `get_network_requests` |
| `direction` | string | no | `"sent"`, `"received"`, or `"both"` (default `"both"`) |
| `limit` | int | no | Max frames to return per direction (default all buffered) |
| `mode` | string | **yes** | `"peek"` (keep frames) or `"drain"` (consume frames) |

Returns: `{sent, received}` arrays of `{opcode, mask?, direction, payload?, payload_base64?, payload_truncated?, time}`.

#### `intercept`

Manage per-tab network request interception rules using CDP's `Fetch` domain. `Fetch.enable` is toggled lazily when the first rule is added and disabled automatically when the last rule is removed or cleared. Applies to the tab's page target (main frame and same-process iframes, not cross-origin OOPIFs). Rules evaluate in insertion order (first match wins); `clear_buffers` does not clear interception rules.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"fulfill"` (mock response), `"modify"` (rewrite request), `"fail"` (abort/block request), `"remove"` (remove rule by ID), `"list"` (list active rules), or `"clear"` (remove all rules) |
| `id` | string | no | Rule ID to remove (required for `action="remove"`) |
| `url_pattern` | string | no | Glob pattern (`*` matches any chars including `/`, `?` matches one char, `\` escapes). Required for `fulfill`/`modify`/`fail`. |
| `method` | string | no | Optional case-insensitive HTTP method filter (`fulfill`/`modify`/`fail`) |
| `resource_type` | string | no | Optional CDP resource type filter (same 20-value lowercase enum as `get_network_requests.type`; Chromium's `Fetch` domain reports `"xhr"` for both `fetch()` and `XMLHttpRequest`) |
| `status` | int | no | HTTP status code (`100`–`599`, default `200`, `fulfill` only; bodyless statuses `1xx`, `204`, `205`, `304` forbid non-empty `body`) |
| `body` | string | no | Response body text or base64 (`fulfill` only) |
| `body_base64` | bool | no | Decode `body` from base64 (`fulfill` only) |
| `headers` | object | no | Response headers (`fulfill`) or case-insensitive request header overrides (`modify`; setting `""` sends an empty header value). Validated per RFC 9110 (valid token names, no case-colliding duplicates, no CR/LF/NUL in values). |
| `url` | string | no | Replacement absolute URL (`modify` only) |
| `override_method` | string | no | Replacement HTTP method token (`modify` only) |
| `post_data` | string | no | Non-empty replacement request body text or base64 (`modify` only) |
| `post_data_base64` | bool | no | Decode `post_data` from base64 (`modify` only) |
| `error_reason` | string | no | CDP `network.ErrorReason` (`fail` only, default `"Failed"`): `"Failed"`, `"Aborted"`, `"TimedOut"`, `"AccessDenied"`, `"ConnectionClosed"`, `"ConnectionReset"`, `"ConnectionRefused"`, `"ConnectionAborted"`, `"ConnectionFailed"`, `"NameNotResolved"`, `"InternetDisconnected"`, `"AddressUnreachable"`, `"BlockedByClient"`, `"BlockedByResponse"` |

Returns: `{id}` for `fulfill`/`modify`/`fail`; `{}` for `remove`; `{rules}` for `list`; `{removed}` for `clear`.

### JavaScript Execution

#### `evaluate`

Execute JavaScript in the page (or a same-origin frame). Without `ref`/`selector`, `expression` is evaluated directly as a JS expression (no top-level `return`). With `ref` or `selector`, `expression` runs as the body of a function with the matched element as `el` — use `return` to produce a value.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `expression` | string | yes | JS expression (or function body using `el` and `return` when `ref`/`selector` is set) |
| `ref` | string | no | Snapshot ref available as `el` (mutually exclusive with `selector`/`frame`) |
| `selector` | string | no | CSS/`>>>` shadow selector; matched element available as `el` |
| `frame` | string | no | Optional same-origin frame ID from `list_frames` |
| `timeout` | int | no | Max time in milliseconds to wait for `selector` (default `5000`) |
| `await_promise` | bool | no | Wait for Promise resolution (default `true`) |
| `max_result_bytes` | int | no | Cap serialized result at this many bytes (`0` = no cap). When exceeded, `result` becomes a UTF-8-safe truncated JSON string prefix with `truncated: true` and `total_bytes`. |

Returns: `{result, truncated?, total_bytes?}`. Thrown JS exceptions return an MCP tool error (`IsError: true`).

### Interaction

All nine tools in this section accept `observe_window_ms` (`0` = off, max `30000`) — on `dialog`, only with `action="accept"` or `action="dismiss"`. Selector-based actions also accept `timeout` (default `5000` ms). `click`, `type`, `select_option`, `press_key`, and `drag_and_drop` run under `runDialogInterruptible` so they return promptly if a synchronous JS modal dialog opens during dispatch instead of hanging on a blocked renderer.

#### `click`

Click an element or viewport coordinates. Probes target state before dispatch: errors before dispatching on `disabled` or `aria-disabled="true"` elements; warns on `pointer-events: none` or `visibility: hidden`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref |
| `selector` | string | no | CSS/`>>>` shadow selector |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `x`, `y` | float | no | Viewport coordinates (both required together; mutually exclusive with `ref`/`selector`/`frame`) |
| `button` | string | no | `"left"` (default), `"right"`, `"middle"` |
| `click_count` | int | no | Number of clicks (`1`–`10`, default `1`; use `2` for double-click) |
| `modifiers` | []string | no | Optional modifier keys: `"ctrl"`, `"shift"`, `"alt"`, `"meta"` |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline activity observation window in ms (`0` = off, max `30000`) |

Returns: `{disabled, aria_disabled, pointer_events, visible, warning?, observed?}`.

#### `type`

Type text into an input or contenteditable element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref |
| `selector` | string | no | CSS/`>>>` shadow selector |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `text` | string | yes | Text to type |
| `mode` | string | **yes** | `"replace"` (clear field first, handling controlled inputs and `contenteditable`) or `"append"` (move caret to end and append) |
| `delay` | int | no | Delay between keystrokes in ms (default `0`; runs outside selector timeout) |
| `submit` | bool | no | Press Enter after typing (default `false`) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `select_option`

Select option(s) on a `<select>` element. Provide exactly one of `values`, `labels`, or `indexes`. On `<select multiple>`, replaces the current selection; pass an empty slice `[]` to deselect all options.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref |
| `selector` | string | no | CSS/`>>>` shadow selector |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `values` | []string | no | Option values to select (`[]` clears all on multi-select) |
| `labels` | []string | no | Option visible texts to select (`[]` clears all on multi-select) |
| `indexes` | []int | no | Option 0-based indexes to select (`[]` clears all on multi-select) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `scroll`

Scroll an element into view (`ref` or `selector` + optional `frame`), or scroll the window (or targeted `frame`) by `(x, y)` pixels.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref to scroll into view |
| `selector` | string | no | CSS/`>>>` shadow selector to scroll into view |
| `frame` | string | no | Optional same-origin frame ID (for `selector`, or to scroll that frame's window) |
| `x`, `y` | int | no | Horizontal/vertical scroll delta in pixels (when `ref` and `selector` are omitted) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{scroll_x, scroll_y, observed?}` (reporting the scrolled frame's offsets when targeting a subframe).

#### `hover`

Move the CDP mouse cursor over an element or viewport `(x, y)` coordinates, firing JS mouse events and CSS `:hover`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref |
| `selector` | string | no | CSS/`>>>` shadow selector |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `x`, `y` | float | no | Viewport coordinates (both required together) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `press_key`

Press a keyboard key (with optional modifiers), optionally focusing a target element first. Suppresses literal character insertion when `ctrl`/`meta`/`alt` modifiers are held.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `key` | string | yes | Key name (e.g. `"Enter"`, `"Tab"`, `"Escape"`, `"ArrowDown"`, `"Space"`, `"Insert"`, `"a"`) |
| `modifiers` | []string | no | Optional modifier keys: `"ctrl"`, `"shift"`, `"alt"`, `"meta"` |
| `ref` | string | no | Optional snapshot ref to focus before pressing the key |
| `selector` | string | no | Optional CSS/`>>>` shadow selector to focus before pressing the key |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `upload_files`

Set files on a file input element. Rejects empty `paths` or empty path entries. For locally launched browsers (`ModeLaunch`), validates absolute existing non-directory local paths; for connected browsers (`ModeConnect`), passes paths directly to Chrome for remote/container filesystems.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `ref` | string | no | Snapshot ref |
| `selector` | string | no | CSS/`>>>` shadow selector |
| `frame` | string | no | Optional same-origin frame ID for `selector` |
| `paths` | []string | yes | Non-empty list of file paths to set |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `drag_and_drop`

Drag from a source onto a drop target using `ref`, CSS/`>>>` selector (+ optional `frame`), or viewport `(x, y)` coordinates independently for source and target. Uses CDP `Input.setInterceptDrags` alongside stepped mouse dispatch so both HTML5 native drag-and-drop (`DataTransfer`) and pointer-based drags work seamlessly.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `source_ref` / `source_selector` / `source_frame` / `source_x` / `source_y` | string / float | yes (one mode) | Source element or viewport coordinates |
| `target_ref` / `target_selector` / `target_frame` / `target_x` / `target_y` | string / float | yes (one mode) | Drop target element or viewport coordinates |
| `steps` | int | no | Intermediate mouse move steps (`0`–`100`, default `10`) |
| `timeout` | int | no | Selector wait timeout in ms (default `5000`) |
| `observe_window_ms` | int | no | Inline observation window in ms (`0` = off, max `30000`) |

Returns: `{observed?}`.

#### `dialog`

Inspect or handle a JavaScript modal dialog (`alert`, `confirm`, `prompt`, `beforeunload`) without blocking on the renderer. Synchronously clears the handled dialog generation via `ClearIfSeq` and re-reads current dialog state after any `observe_window_ms` so chained dialogs are reflected immediately.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"get"`, `"accept"`, or `"dismiss"` |
| `text` | string | no | For `action="accept"` on a prompt: text to submit (omit to keep `default_prompt`; pass `""` for empty string; forbidden for `get`/`dismiss`) |
| `observe_window_ms` | int | no | For `action="accept"`/`"dismiss"`: inline observation window in ms (`0` = off, max `30000`; forbidden for `get`) |

Returns: `{open, dialog?, observed?}` where `dialog` contains `{type, message, default_prompt?, url?, has_browser_handler, opened_at}`.

### Cookies & Storage

#### `cookies`

Get, set, or delete browser cookies.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"get"`, `"set"`, or `"delete"` |
| `urls` | []string | no | For `action="get"`: filter to these URLs (defaults to current page URL) |
| `limit` | int | no | For `action="get"`: max cookies to return (default `100`) |
| `name` | string | no | Cookie name (required for `set`; optional filter for `delete`) |
| `value` | string | no | Cookie value (required for `set`; empty string allowed) |
| `domain` | string | no | Cookie domain (defaults to current HTTP(S) URL for `set`; optional scope for `delete` — leading `.` matches apex and subdomains; when omitted with `name`/`path`, scopes to current HTTP(S) host) |
| `path` | string | no | Cookie path (default `"/"` for `set`; optional exact filter for `delete`) |
| `expires` | float | no | For `action="set"`: Unix timestamp expiration (omit for session cookie) |
| `http_only` | bool | no | For `action="set"`: HTTP-only flag (default `false`) |
| `secure` | bool | no | For `action="set"`: Secure flag (default `false`) |
| `same_site` | string | no | For `action="set"`: `"Strict"`, `"Lax"`, or `"None"` |

Deleting with `name`, `domain`, and `path` all omitted clears all browser cookies.
Returns: `{cookies, total, truncated?}` for `get`; `{}` for `set`/`delete`.

#### `storage`

Read, write, or delete items in `localStorage` or `sessionStorage`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"get"`, `"set"`, or `"delete"` |
| `type` | string | no | `"local"` (default) or `"session"` |
| `key` | string | no | Key to read, write (required for `set`), or delete (omit on `delete` to clear all items) |
| `value` | string | no | Value to write (required for `set`; empty string allowed) |
| `keys_only` | bool | no | For `action="get"`: list `{key, size}` pairs without values (cannot combine with `key` or `max_bytes`) |
| `limit` | int | no | For `action="get"` (without `key`): max keys/entries to return (default all) |
| `max_bytes` | int | no | For `action="get"` (`keys_only=false`): max UTF-8 bytes of returned data (value bytes for single-key, key+value sum for all-entries; default `65536`) |

Returns:
- `get` single-key: `{value, total_bytes?, truncated?}` (`value` is `null` if absent).
- `get` `keys_only=true`: `{keys, total, truncated?}` sorted deterministically by key.
- `get` all-entries: `{entries, total, truncated?}` (sorted deterministically; `total` always reports total stored key count).
- `set`/`delete`: `{}`.

#### `indexeddb`

Inspect or delete IndexedDB data on the current page's origin via CDP `IndexedDB` domain (rejects opaque origins such as `about:blank`).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"get"` or `"delete"` |
| `database` | string | no | Database name (required for `delete`; omit on `get` to list all database names) |
| `store` | string | no | Object store name (requires `database`). On `get`, reads paginated records; on `delete`, clears that store instead of deleting the database. |
| `index` | string | no | For `action="get"`: optional index name within `store` (requires `store`) |
| `skip` | int | no | For `action="get"`: records to skip (default `0`, requires `store`) |
| `limit` | int | no | For `action="get"`: max records to return (default `50`, requires `store`) |

Returns: `{databases}` (list DBs), `{database, version, stores}` (inspect schema), or `{database, store, index?, entries, has_more}` (read records) for `get`; `{}` for `delete`.

#### `cache_storage`

Inspect or delete Cache Storage (`window.caches`) on the current page's origin via CDP `CacheStorage` domain (rejects opaque origins).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `action` | string | yes | `"get"` or `"delete"` |
| `cache_name` | string | no | Cache name (required for `delete`; omit on `get` to list all caches) |
| `request_url` | string | no | Exact request URL (requires `cache_name`). On `get`, reads the cached response body; on `delete`, deletes only that entry. |
| `path_filter` | string | no | For `action="get"`: case-insensitive URL path filter when listing entries (cannot combine with `request_url`) |
| `skip` | int | no | For `action="get"`: entries to skip when listing (default `0`, cannot combine with `request_url`) |
| `limit` | int | no | For `action="get"`: max entries when listing (default `50`, cannot combine with `request_url`) |
| `range_start` | int | no | For `action="get"`: byte offset start for response body slicing (default `0`, requires `request_url`) |
| `range_end` | int | no | For `action="get"`: exclusive byte offset end (`0` = end, requires `request_url`) |

Returns: `{caches}`, `{cache_name, entries, total, has_more}`, or `{cache_name, request_url, status, status_text?, response_type?, response_headers?, body, base64_encoded, total_bytes, truncated}` for `get`; `{}` for `delete`.

### Performance

#### `get_performance`

Get Chrome runtime performance metrics (`JSHeapUsedSize`, `Nodes`, `LayoutCount`, `ScriptDuration`, etc.), buffered layout shifts with W3C session-window CLS (max 5s window / 1s gap, excluding `had_recent_input` shifts), and Largest Contentful Paint (LCP) timeline entries. Serializes ephemeral `Performance.enable`/`getMetrics`/`disable` per tab via `perfOpMu` and fails fast (without draining buffered shifts/LCP) if a JavaScript modal dialog is open.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `mode` | string | no | `"peek"` (default, keep buffered shifts/LCP) or `"drain"` (consume buffered shifts/LCP) |
| `limit` | int | no | Max `lcp` entries to return (default all; `layout_shifts` stays unbounded so CLS is exact) |

Returns: `{metrics, layout_shifts, cls, lcp}` (all arrays non-null `[]` when empty).

### Downloads

When `--download-dir` is configured, Chrome automatically saves downloaded files, tracks them via a single browser-level CDP listener, and renames GUID files to non-overwriting suggested filenames asynchronously. When `--download-dir` is empty, `get_downloads` is not registered.

#### `get_downloads`

Get tracked file downloads with status, progress, and file paths. Downloads are browser-scoped (not per-tab).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID (defaults to active browser) |
| `mode` | string | **yes** | `"peek"` (keep completed downloads) or `"drain"` (consume completed downloads). `in_progress` is always returned by snapshot. |
| `limit` | int | no | Max completed entries to return (default all) |

Returns: `{downloads, in_progress}`.

### Emulation & Page Configuration

#### `emulate`

Emulate browser/device environment settings on a tab. Parameters from other actions are rejected cleanly so callers get immediate feedback on typos.

| Parameter | Type | Action | Description |
|-----------|------|--------|-------------|
| `tab` | string | all | Tab ID (optional) |
| `action` | string | all (required) | `"viewport"`, `"device"`, `"network"`, `"geolocation"`, `"timezone"`, `"locale"`, `"user_agent"`, `"cpu_throttling"`, `"vision_deficiency"`, `"media"`, or `"pseudo_state"` |
| `width`, `height`, `device_scale_factor`, `mobile` | int / float / bool | `viewport` | Viewport `width` and `height` (required, `> 0`), scale factor (default `1.0`), mobile flag |
| `device`, `landscape` | string / bool | `device` | Preset name (required: `"iPhone SE"`, `"iPhone 13"`, `"iPhone 14"`, `"iPhone 14 Pro"`, `"iPhone 15"`, `"iPhone 15 Pro"`, `"iPhone 15 Pro Max"`, `"iPhone 16"`, `"iPhone 16 Pro"`, `"iPhone 16 Pro Max"`, `"iPad"`, `"iPad Mini"`, `"iPad Pro 11"`, `"Pixel 5"`, `"Galaxy S9+"`, `"Galaxy Tab S4"`, or `"reset"`) and optional `landscape` (forbidden with `"reset"`) |
| `offline`, `latency`, `download_throughput`, `upload_throughput` | bool / float | `network` | Network conditions (`-1` or `0` disables throughput cap; defaults reset) |
| `latitude`, `longitude`, `accuracy` | float | `geolocation` | Mock coordinates (`-90..90`, `-180..180`, accuracy default `1`m); omit all three to reset |
| `timezone_id` | string | `timezone` | IANA timezone ID (e.g. `"America/New_York"`; empty resets) |
| `locale` | string | `locale` | ICU locale ID (e.g. `"en_US"`; empty resets) |
| `user_agent`, `accept_language`, `platform` | string | `user_agent` | `user_agent` string (required; empty resets), optional `accept_language` and `platform` |
| `rate` | float | `cpu_throttling` | Slowdown factor (required; `1` = disabled, values in `[0, 1)` clamp to `1`, `< 0` rejected) |
| `type` | string | `vision_deficiency` | Deficiency type (required): `"none"`, `"blurredVision"`, `"reducedContrast"`, `"achromatopsia"`, `"deuteranopia"`, `"protanopia"`, `"tritanopia"` |
| `media`, `features` | string / array | `media` | CSS media type (`"screen"`, `"print"`, or `""` to reset) and `{name, value}` media features (e.g. `prefers-color-scheme: dark`, `prefers-reduced-motion: reduce`) |
| `ref`, `selector`, `frame`, `states`, `timeout` | string / []string / int | `pseudo_state` | Target element (required: `ref`, or CSS/`>>>` `selector` + optional `frame`), `states` (`"active"`, `"focus"`, `"hover"`, `"visited"`, `"focus-within"`, `"focus-visible"`, `"target"`; omit or `[]` to clear), and optional selector `timeout` (default `5000` ms). Keeps `CSS` domain enabled only while forced states are active and disables automatically when cleared or on cross-document navigation. |

Returns: `{device?, width?, height?, device_scale_factor?, mobile?, touch?, landscape?, user_agent?, states?}` (`device` returns applied metrics; `pseudo_state` returns `states`; other actions return `{}`).

#### `configure_page`

Configure tab-level page behavior. Parameters from other actions are rejected cleanly.

| Parameter | Type | Action | Description |
|-----------|------|--------|-------------|
| `tab` | string | all | Tab ID (optional) |
| `action` | string | all (required) | `"add_script"`, `"remove_script"`, `"set_headers"`, `"set_permission"`, or `"set_ignore_cert_errors"` |
| `source`, `evaluate_now` | string / bool | `add_script` | `source` JS code (required) evaluated on every new document; `evaluate_now` (required) also runs once on the current document. Returns `{identifier, warning?}` (`warning` set if `evaluate_now` failed while registration succeeded). |
| `identifier` | string | `remove_script` | Script identifier returned by `add_script` (required) |
| `headers` | object | `set_headers` | Map of custom HTTP headers injected into all requests (required; pass `{}` to clear). Validated per RFC 9110 (valid token names, no case-colliding duplicates, no CR/LF/NUL in values). |
| `name`, `setting`, `origin` | string | `set_permission` | Permission `name` (required, e.g. `geolocation`, `notifications`, `clipboard-read`), `setting` (required: `"granted"`, `"denied"`, `"prompt"`), and optional `origin` |
| `ignore` | bool | `set_ignore_cert_errors` | Required boolean toggling TLS certificate error ignoring |

Returns: `{identifier?, warning?}` for `add_script`; `{}` for other actions.

### Observation

Observation detects a **silent no-op**: an action that dispatches cleanly and reports success while the page does nothing. Observation is **not a standalone tool** — it is an `observe_window_ms` parameter on all nine action tools (`click`, `type`, `select_option`, `scroll`, `hover`, `press_key`, `upload_files`, `drag_and_drop`, and `dialog` on `accept`/`dismiss`), measured inline inside the handler so LLM round-trip latency never misses short-lived effects.

#### Window boundaries

The window **opens after the tool has waited for its target element, immediately before dispatch**, and closes `observe_window_ms` after dispatch returns (capped at `30000` ms). `window_ms` reports the real elapsed span (dispatch + wait), captured immediately after the timer completes before reading collector buffers and frame mutations.

DOM mutations are anchored in each frame's own `performance.now()` clock pinned by CDP's unique execution context ID, watching `childList` mutations across all reachable frames. In-flight HTTP requests and newly opened WebSocket connections started within the window are counted even if they complete after the window closes.

#### The `observed` object

```
{window_ms, network_requests, dom_mutations, url_changed, console_messages, js_errors,
 has_any_effect, truncated?, dom_mutations_unavailable?, dom_mutations_partial?}
```

Present only when `observe_window_ms > 0`. `has_any_effect` is `network_requests > 0 || url_changed || console_messages > 0 || js_errors > 0` (`dom_mutations` is intentionally excluded because ambient `childList` churn is noisy).

| Flag | Meaning |
|------|---------|
| `truncated` | Tab closed or client disconnected before the window elapsed; counts are partial and `has_any_effect: false` proves nothing. |
| `dom_mutations_unavailable` | Top-frame document navigated mid-window, a JS modal dialog blocked renderer evaluation (fast short-circuit via `Tab.Dialog`), or the page could not be read. Use `url_changed` or `dialog(action="get")`. |
| `dom_mutations_partial` | `dom_mutations` is a lower bound because an out-of-process cross-origin iframe, newly created frame, or navigated subframe could not be observed. |

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
      browser.go        # Browser type: launch/connect lifecycle, popup & target adoption, download collector
      manager.go        # browser.Manager: registry of browsers, MRU active browser tracking
    tab/
      tab.go            # Tab type: target context, collectors, dialog/crash/nav listeners, LinkContext
      manager.go        # tab.Manager: per-browser tab registry, MRU active tab tracking, BringToFront
      emulation.go      # Per-tab CSS forced pseudo-state lifecycle and navigation cleanup
      intercept.go      # Lazy Fetch domain toggle and async request interception dispatch
      performance.go    # Serialized Performance.enable/getMetrics/disable with dialog interruption
      snapshot.go       # Per-tab accessibility snapshot ref store and navigation invalidation
    profile/
      profile.go        # Chrome profile discovery from Local State, platform user data dir detection
    collector/
      collector.go      # Generic RingBuffer[T] with filtered drain/peek/clear and shared UTF-8 helpers
      console.go        # Console log collector (runtime.EventConsoleAPICalled)
      dialog.go         # Active JS modal dialog state and OpeningNotify() channel per tab
      download.go       # Browser-scoped download tracking and async non-overwriting renaming
      errors.go         # JS error collector (runtime.EventExceptionThrown)
      execcontext.go    # Per-frame main-world JS execution contexts and frame hierarchy
      intercept.go      # Ordered per-tab interception rules, glob matching, and header merging
      network.go        # Network request/response collector (redirects, timing, headers)
      performance.go    # Performance timeline collector (LCP, layout shifts, W3C session-window CLS)
      time.go           # CDP timestamp conversion helpers
      urlchange.go      # Main-frame navigation and SPA URL-change collector
      websocket.go      # WebSocket connection and per-direction frame ring collector
    tools/
      tools.go          # Exported Options/Register, immutable resolvedOptions, shared helpers, schema builder
      target.go         # Unified ref / CSS+>>> Shadow DOM / frame / (x, y) element resolver & dialog interruption
      browser.go        # browser_launch, browser_connect, browser_activate, browser_close, browser_list
      profile.go        # browser_list_profiles
      tabs.go           # tab_new, tab_list, tab_activate, tab_close
      navigation.go     # navigate, wait_for
      visual.go         # screenshot, pdf, atomic writeDownloadFile
      snapshot.go       # snapshot (compact AX tree outline + ref generation)
      dom.go            # query, get_dom
      frames.go         # list_frames and same-origin iframe helpers
      console.go        # get_console_logs, get_js_errors
      buffers.go        # clear_buffers
      network.go        # get_network_requests, get_network_body, get_websocket_frames
      intercept.go      # intercept tool and shared RFC 9110 HTTP header validation
      js.go             # evaluate
      interaction.go    # click, type, select_option, scroll, hover, press_key, upload_files
      drag.go           # drag_and_drop
      dialog.go         # dialog
      observation.go    # Inline activity observation window helpers
      cookies.go        # cookies
      storage.go        # storage
      indexeddb.go      # indexeddb
      cachestorage.go   # cache_storage
      performance.go    # get_performance
      downloads.go      # get_downloads
      config.go         # configure_page
      emulation.go      # emulate
```

The `internal/tools` package exports only `Options` and `Register`; all tool input/output types and helpers are package-private, and `Register` copies `Options` into an immutable `resolvedOptions` value without mutating the caller's struct.

### Key Components & Concurrency

- **`browser.Browser`** — Manages one Chrome instance (`ModeLaunch` or `ModeConnect`), its allocator and browser CDP contexts, its `tab.Manager`, its browser-scoped `collector.Download`, and browser-level `Target.targetCreated` popup auto-adoption.
- **`browser.Manager`** — Thread-safe (`sync.Mutex`) MRU registry of managed browsers with dead-browser pruning on access and lock-free teardown.
- **`tab.Tab`** — Wraps a `chromedp` target `context.Context`, owns all per-tab collectors, and provides fine-grained per-tab operation locks (`interceptOpMu`, `cssOpMu`/`cssStateMu`, `perfOpMu`, `snapshotMu`, `dragOpMu`) so stateful multi-step CDP flows serialize cleanly without blocking independent tabs.
- **`tab.Manager`** — Thread-safe (`sync.RWMutex`) MRU per-browser tab registry with external target-close pruning and `Page.bringToFront` on activation.
- **`collector.RingBuffer[T]`** — Generic bounded mutex-protected buffer supporting filtered `Drain` (consuming only returned matches within `limit`), `Peek`, and zeroing `Clear`.

### Lifecycle & Error Handling

1. **Startup**: Parse flags, trap `SIGINT`/`SIGTERM`, create empty `browser.Manager`, register tools on MCP `Server`, and run `StdioTransport`.
2. **Tool execution**: Validate parameters before tab resolution, resolve target browser/tab (explicit ID → active), link MCP request cancellation onto the tab context (`tab.LinkContext`), execute CDP actions, and return structured results. All recoverable errors return MCP tool errors (`IsError: true`) rather than protocol errors so the LLM can reason and retry.
3. **Shutdown**: Signal cancellation terminates `srv.Run` cleanly (exit 0) and triggers `defer mgr.CloseAll()` — detaching adopted user tabs in connected browsers while closing server-created tabs and killing launched Chrome processes.

## Tool Annotations

All registered tools carry explicit MCP `ToolAnnotations` (including non-nil `destructiveHint`):

| Tool(s) | `readOnlyHint` | `destructiveHint` | `idempotentHint` |
|---------|----------------|-------------------|------------------|
| `browser_list`, `browser_list_profiles`, `tab_list`, `wait_for`, `snapshot`, `query`, `get_dom`, `list_frames`, `get_network_body` | true | false | true |
| `screenshot`, `pdf` (without `--download-dir`) | true | false | true |
| `screenshot`, `pdf` (with `--download-dir`) | false | false | true |
| `get_console_logs`, `get_js_errors`, `get_network_requests`, `get_websocket_frames`, `get_performance`, `get_downloads` | false | false | false |
| `browser_launch`, `browser_connect`, `tab_new`, `navigate`, `evaluate`, `click`, `type`, `select_option`, `scroll`, `hover`, `press_key`, `upload_files`, `drag_and_drop`, `dialog` | false | false | false |
| `browser_activate`, `tab_activate`, `clear_buffers`, `emulate` | false | false | true |
| `browser_close`, `tab_close`, `intercept`, `cookies`, `storage`, `indexeddb`, `cache_storage`, `configure_page` | false | true | false |

Multi-action tools that include destructive delete/remove actions (`intercept`, `cookies`, `storage`, `indexeddb`, `cache_storage`, `configure_page`) advertise `destructiveHint: true` per MCP's additive-only contract for `destructiveHint: false`.

## Configuration

```
chromedp-mcp [--download-dir <path>] [--allowed-profiles <names>] [--user-data-dir <path>]
```

| Flag | Description |
|------|-------------|
| `--download-dir` | Directory for saving screenshots, PDFs, and downloads (supports `~` expansion). When set, registers `get_downloads`, enables Chrome file downloads with tracking/renaming, and allows `screenshot`/`pdf` `filename` writes (atomic `0o600` writes with path-traversal rejection). |
| `--allowed-profiles` | Comma-separated list of Chrome profile display names (e.g. `"Work,Personal"`). Registers `browser_list_profiles` and exposes `profile` on `browser_launch`. |
| `--user-data-dir` | Overrides the auto-detected Chrome/Chromium user data directory for profile discovery (supports `~` expansion; only relevant when `--allowed-profiles` is set). |

When `--download-dir` is omitted, `get_downloads` is not registered, `screenshot`/`pdf` advertise `readOnlyHint: true` and return binary data inline only, and passing `filename` returns an error.

### User Profile Security

> **Warning:** Enabling `--allowed-profiles` gives the LLM access to real Chrome profiles, including saved cookies, active sessions, `localStorage`, and extension data. Only expose profiles you understand the implications of, and never expose profiles containing sensitive credentials to untrusted LLM providers.

Profile access is disabled by default. When `--allowed-profiles` is omitted, `browser_list_profiles` is not registered, `browser_launch` omits `profile` from its schema and rejects it at runtime, and every launched browser uses an ephemeral temporary profile discarded on close. Note that Chrome enforces a singleton lock on user data directories, so Chrome must not already be running against the same `--user-data-dir`.

## Future Features

Capabilities supported by CDP and candidates for future additions:

- **Certificate Inspection** — Site certificate details via `network.GetCertificate`.
- **WebAuthn** — Virtual authenticators and FIDO2/passkey testing via `webauthn` domain.
- **Media & Web Audio Inspection** — Playback/codec/buffer diagnostics via `media` and `webaudio` domains.
- **Service Workers & PWA** — Service worker lifecycle and web app manifest inspection via `serviceworker` and `pwa` domains.
- **Deep Profiling & Tracing** — CPU profiles (`profiler`), heap snapshots (`heapprofiler`), and Chrome trace events (`tracing`).
- **Out-of-Process Iframe (OOPIF) Attachment** — Auto-attaching to cross-origin iframe CDP sessions for full DOM observation and targeting.
