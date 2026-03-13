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

The browser lifecycle is entirely tool-driven. The LLM decides when to launch or connect to a browser, and with what settings. The only CLI-level configuration that affects browser launches is `--allowed-profiles`, which gates access to real Chrome user profiles (see [User Profile Security](#user-profile-security)).

This means the LLM can:
- Launch a headless browser for automated testing
- Connect to a developer's running browser to inspect a live session
- Switch between browsers mid-conversation
- Tear down and re-launch with different settings

### Browser Management Tools

#### `browser_launch`

Launch a new Chrome instance managed by the server.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `headless` | bool | no | Run in headless mode (default `true`) |
| `width` | int | no | Initial viewport width (default 1920) |
| `height` | int | no | Initial viewport height (default 1080) |
| `profile` | string | no | Chrome profile display name to use (e.g. `"Work"`). Must be in the allowed profiles list. Use `browser_list_profiles` to see available profiles. Requires `--allowed-profiles` to be configured. |

Returns: browser ID.

When `profile` is specified, the browser is launched with `--user-data-dir` and `--profile-directory` pointing to the real Chrome profile. This gives the browser access to the profile's cookies, sessions, extensions, and other stored data. When omitted, a temporary ephemeral profile is used (default behavior).

Closing the server kills any launched browsers.

#### `browser_list_profiles`

List available Chrome/Chromium user profiles that can be used with `browser_launch`. Only registered when `--allowed-profiles` is configured. Returns only profiles that are in the allowed list.

No parameters.

Returns: array of `{name, dir}` objects where `name` is the display name and `dir` is the profile directory name.

#### `browser_connect`

Connect to an already-running Chrome instance via its remote debugging URL.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | yes | Chrome remote debugging URL (`ws://` or `http://`) |

Returns: browser ID.

The server does not own the browser lifecycle in this mode. Disconnecting does not kill Chrome.

#### `browser_close`

Close a browser. In launch mode, kills the Chrome process. In connect mode, disconnects.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. If omitted, closes the active browser. |

#### `browser_list`

List all managed browsers with their IDs, modes, and connection status.

No parameters.

### Active Browser

When multiple browsers are managed, one is the **active browser**. Tab tools that don't specify a browser operate on the active browser. The most recently launched/connected browser becomes active. If the active browser is closed, the most recently used remaining browser becomes active.

## Tab Model

The server supports multiple concurrent tabs per browser. Each tab is identified by a string **tab ID** returned on creation. Most tools accept an optional `tab` parameter; when omitted, the tool operates on the **active tab** of the active browser.

Internally, each tab maps to a `chromedp.Context` derived from the browser context. Creating a tab creates a new Chrome target; closing a tab cancels its context.

### Tab Management Tools

#### `tab_new`

Create a new tab, optionally navigating to a URL.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | no | URL to navigate to |
| `browser` | string | no | Browser ID. Defaults to active browser. |

Returns: tab ID. The new tab becomes the active tab.

#### `tab_list`

List all open tabs with their IDs, URLs, and titles.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. Defaults to active browser. |

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

No implicit auto-creation. If no browser exists when a tool that requires one is called, an error is returned telling the LLM to call `browser_launch` or `browser_connect`. Similarly, if no tab exists, the error tells the LLM to call `tab_new`.

## Event Collectors

The server registers CDP event listeners per tab that buffer events. These start automatically when a tab is created.

### Collected Event Types

| Collector | CDP Events | Buffer Behavior |
|-----------|-----------|-----------------|
| **Console** | `runtime.EventConsoleAPICalled` | Ring buffer (default 1000 entries) |
| **JS Errors** | `runtime.EventExceptionThrown` | Ring buffer (default 500 entries) |
| **Network** | `network.EventRequestWillBeSent`, `network.EventResponseReceived`, `network.EventLoadingFinished`, `network.EventLoadingFailed` | Ring buffer of request/response pairs (default 1000 entries). Response bodies captured lazily on demand. |
| **Performance Timeline** | `performancetimeline.EventTimelineEventAdded` | Ring buffer, captures LCP and layout shift entries |

Each collector supports two read modes:
- **Drain**: Return and clear all buffered entries since the last drain. Default mode.
- **Peek**: Return entries without clearing. Specified via `peek: true` parameter.

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

Returns: final URL (after redirects), page title, HTTP status code.

**Implementation note**: `"networkidle"` uses Chrome's built-in `page.EventLifecycleEvent` with `Name == "networkIdle"` (0 in-flight requests for 500ms), not a custom heuristic.

#### `reload`

Reload the current page.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `bypass_cache` | bool | no | Bypass browser cache (default `false`) |

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

### Visual Feedback

#### `screenshot`

Capture a screenshot.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to screenshot a specific element. If omitted, captures the viewport. |
| `full_page` | bool | no | Capture the full scrollable page instead of just the viewport (default `false`). Ignored if `selector` is set. |
| `format` | string | no | `"png"` (default) or `"jpeg"` |
| `quality` | int | no | JPEG quality 1-100 (default 80). Ignored for PNG. |
| `filename` | string | no | Save to disk with this filename (requires `--download-dir`). Timestamp-based name used if empty. The image is still returned inline. |

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
| `filename` | string | no | Save to disk with this filename (requires `--download-dir`). Timestamp-based name used if empty. |

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
| `selector` | string | yes | CSS selector |
| `limit` | int | no | Max elements to return (default 10) |
| `attributes` | bool | no | Include element attributes (default `true`) |
| `computed_style` | []string | no | List of CSS property names to include computed values for |
| `outer_html` | bool | no | Include outer HTML (default `false`) |
| `text` | bool | no | Include text content (default `true`) |
| `bbox` | bool | no | Include bounding box dimensions (default `false`) |

Returns: array of matched elements with the requested fields.

#### `get_html`

Get the HTML content of the page or a subtree.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to scope to a subtree. If omitted, returns the full page. |
| `outer` | bool | no | Return outer HTML (default `true`). If `false`, returns inner HTML. |

#### `get_text`

Get the visible text content of the page or an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector. If omitted, returns text of the entire page body. |

#### `get_accessibility_tree`

Get the accessibility tree of the page. This is a compact representation of the page structure that is much more token-efficient than raw HTML. Uses CDP's `accessibility.GetFullAXTree` (or `GetPartialAXTree` when scoped to a selector).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to scope to a subtree |
| `depth` | int | no | Max tree depth (default unlimited) |
| `interesting_only` | bool | no | Filter to only "interesting" nodes — those with a role, name, or value (default `true`) |

Returns: the accessibility tree as structured JSON. Each node includes: `role`, `name`, `value`, `description`, `properties` (states like `focused`, `checked`, `expanded`, etc.), and `children`.

### Console & Errors

#### `get_console_logs`

Get captured console messages.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `level` | string | no | Filter by level: `"log"`, `"warn"`, `"error"`, `"info"`, `"debug"`. If omitted, returns all. |
| `peek` | bool | no | If `true`, don't clear the buffer (default `false`) |
| `limit` | int | no | Max entries to return (default all) |

Returns: array of `{level, text, timestamp, source}` objects.

#### `get_js_errors`

Get captured JavaScript exceptions.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `peek` | bool | no | If `true`, don't clear the buffer (default `false`) |
| `limit` | int | no | Max entries to return (default all) |

Returns: array of `{message, source, line, column, stack_trace, timestamp}` objects.

#### `clear_console`

Clear the console and JS error buffers for a tab.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

### Network Inspection

#### `get_network_requests`

Get captured network requests.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `peek` | bool | no | If `true`, don't clear the buffer (default `false`) |
| `limit` | int | no | Max entries to return (default all) |
| `type` | string | no | Filter by resource type: `"document"`, `"stylesheet"`, `"script"`, `"image"`, `"xhr"`, `"fetch"`, `"websocket"`, `"other"` |
| `status_min` | int | no | Filter by minimum HTTP status code |
| `status_max` | int | no | Filter by maximum HTTP status code |
| `url_pattern` | string | no | Filter by URL substring match |
| `failed_only` | bool | no | Return only failed requests (default `false`) |

Returns: array of request objects with `{id, url, method, status, type, timing, request_headers, response_headers, size, error}`.

#### `get_response_body`

Get the response body of a specific network request.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `request_id` | string | yes | The request ID from `get_network_requests` |

Returns: the response body as text, or base64 for binary responses.

### JavaScript Execution

#### `evaluate`

Execute JavaScript in the page context. When a selector is provided, the first matched element is available as `el`. Use `return` to produce a value (e.g. `return el.textContent`).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `expression` | string | yes | JavaScript expression to evaluate. When selector is set, the element is available as `el`. Use `return` to produce a value. |
| `selector` | string | no | CSS selector. If provided, the first matched element is available as `el` in the expression. |
| `timeout` | int | no | Max time in milliseconds to wait for selector (default 5000). Only used when `selector` is set. |
| `await_promise` | bool | no | If the expression returns a Promise, wait for it to resolve (default `true`) |

Returns: the evaluation result as JSON, or an error description if the evaluation threw.

### Interaction

#### `click`

Click an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to click |
| `button` | string | no | Mouse button: `"left"` (default), `"right"`, `"middle"` |
| `click_count` | int | no | Number of clicks (default 1, use 2 for double-click) |

#### `type`

Type text into an element matching a selector.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the input element |
| `text` | string | yes | Text to type |
| `clear` | bool | no | Clear the field before typing (default `false`) |
| `delay` | int | no | Delay between keystrokes in milliseconds (default 0) |

#### `select_option`

Select an option from a `<select>` element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the `<select>` element |
| `value` | string | no | Option value to select |
| `label` | string | no | Option visible text to select |
| `index` | int | no | Option index to select |

Exactly one of `value`, `label`, or `index` must be provided. For `<select multiple>` elements, each call adds to the selection (does not deselect existing options).

#### `submit_form`

Submit a form.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the form or an element within the form |

#### `scroll`

Scroll the page or an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | no | CSS selector to scroll into view. If omitted, scrolls the page. |
| `x` | int | no | Horizontal scroll offset in pixels (when `selector` is omitted) |
| `y` | int | no | Vertical scroll offset in pixels (when `selector` is omitted) |

If `selector` is provided, scrolls the element into view. If omitted, scrolls the page by the specified offsets.

#### `hover`

Hover over an element. Moves the CDP mouse cursor to the element's center, which activates both JavaScript event listeners (`mouseover`, `mouseenter`) and the CSS `:hover` pseudo-class.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to hover over |

#### `focus`

Focus an element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the element to focus |

#### `press_key`

Press a keyboard key.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `key` | string | yes | Key to press (e.g., `"Enter"`, `"Tab"`, `"Escape"`, `"ArrowDown"`) |
| `modifiers` | []string | no | Modifier keys: `"ctrl"`, `"shift"`, `"alt"`, `"meta"` |

#### `upload_files`

Set files on a file input element.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `selector` | string | yes | CSS selector of the file input element |
| `paths` | []string | yes | Absolute file paths to set |

#### `handle_dialog`

Handle a JavaScript dialog (alert, confirm, prompt, beforeunload).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `accept` | bool | yes | Accept or dismiss the dialog |
| `text` | string | no | Text to enter in a prompt dialog |

### Cookies

#### `get_cookies`

Get browser cookies.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `urls` | []string | no | Filter cookies to these URLs. If omitted, returns cookies for the current page URL. |

Returns: array of cookie objects with `{name, value, domain, path, expires, size, httpOnly, secure, sameSite}`.

#### `set_cookie`

Set a browser cookie.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `name` | string | yes | Cookie name |
| `value` | string | yes | Cookie value |
| `domain` | string | no | Cookie domain |
| `path` | string | no | Cookie path (default `"/"`) |
| `expires` | float | no | Cookie expiration as Unix timestamp. If omitted, creates a session cookie. |
| `http_only` | bool | no | HTTP-only flag (default `false`) |
| `secure` | bool | no | Secure flag (default `false`) |
| `same_site` | string | no | SameSite attribute: `"Strict"`, `"Lax"`, `"None"` |

#### `delete_cookies`

Delete cookies by name, or clear all cookies when name is omitted.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `name` | string | no | Cookie name to delete. If omitted, deletes all cookies. |
| `domain` | string | no | Scope deletion to a domain |
| `path` | string | no | Scope deletion to a path |

### Performance & Diagnostics

#### `get_performance_metrics`

Get Chrome runtime performance metrics.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |

Returns: object with metrics including `JSHeapUsedSize`, `JSHeapTotalSize`, `Documents`, `Nodes`, `LayoutCount`, `RecalcStyleCount`, `LayoutDuration`, `ScriptDuration`, `TaskDuration`, etc.

#### `get_layout_shifts`

Get Cumulative Layout Shift (CLS) data.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `peek` | bool | no | If `true`, don't clear the buffer (default `false`) |

Returns: array of layout shift entries with `{value, sources, timestamp}`.

#### `get_coverage`

Get CSS and JavaScript code coverage data.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `type` | string | no | `"css"`, `"js"`, or `"all"` (default `"all"`) |

Returns: per-file coverage data with used/unused byte ranges and percentage.

### Downloads

When `--download-dir` is configured, Chrome automatically saves downloaded files. Downloads are tracked via CDP events and files are renamed from their internal GUID names to their suggested filenames on completion.

#### `get_downloads`

Get tracked file downloads with their status, progress, and file paths. Shows both completed and in-progress downloads. Downloads are browser-scoped (not per-tab).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `browser` | string | no | Browser ID. Defaults to active browser. |
| `peek` | bool | no | If true, do not clear the buffer (default `false`) |
| `limit` | int | no | Max entries to return (default all) |

Returns: `downloads` (completed/canceled entries) and `in_progress` (currently downloading).

### Configuration & Emulation

#### `add_script`

Inject JavaScript to run on every new document before any page scripts. Useful for test fixtures, polyfills, disabling animations, or intercepting APIs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `source` | string | yes | JavaScript source code to evaluate on every new document |

Returns: `identifier` (string) for use with `remove_script`.

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

Block URLs matching patterns. Supports `*` wildcards.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `tab` | string | no | Tab ID |
| `patterns` | []string | yes | URL patterns to block. Pass empty array to clear. |

## Internal Architecture

### Package Structure

```
chromedp-mcp/
  cmd/
    chromedp-mcp/
      main.go           # Entry point, MCP server setup, tool registration, stdio transport
  internal/
    browser/
      browser.go        # Browser type: launch/connect lifecycle, manages allocator context
      manager.go        # BrowserManager: registry of browsers, active browser tracking
    tab/
      tab.go            # Tab type: wraps chromedp.Context, owns event collectors
      manager.go        # TabManager: per-browser tab registry, active tab tracking
    profile/
      profile.go        # Chrome profile discovery from Local State, platform-specific user data dir detection
    collector/
      collector.go      # Generic RingBuffer[T] with drain/peek/filter
      console.go        # Console log collector (runtime.EventConsoleAPICalled)
      errors.go         # JS error collector (runtime.EventExceptionThrown)
      network.go        # Network request/response collector (request lifecycle events)
      performance.go    # Performance timeline collector (LCP, layout shifts)
    tools/
      browser.go        # browser_launch, browser_connect, browser_close, browser_list
      profile.go        # browser_list_profiles
      tabs.go           # tab_new, tab_list, tab_activate, tab_close
      navigation.go     # navigate, reload, go_back, go_forward, wait_for
      visual.go         # screenshot, pdf, set_viewport
      dom.go            # query, get_html, get_text, get_accessibility_tree
      console.go        # get_console_logs, get_js_errors, clear_console
      network.go        # get_network_requests, get_response_body
      js.go             # evaluate (with optional selector)
      interaction.go    # click, type, select_option, submit_form, scroll, hover, focus, press_key, upload_files, handle_dialog
      cookies.go        # get_cookies, set_cookie, delete_cookies
      performance.go    # get_performance_metrics, get_layout_shifts, get_coverage
      emulation.go      # set_geolocation, set_timezone, set_locale, set_user_agent, set_cpu_throttling, set_vision_deficiency, emulate_network, block_urls
```

### Key Types

```go
// browser.Browser manages a single Chrome browser instance.
type Browser struct {
    ID          string
    allocCtx    context.Context
    allocCancel context.CancelFunc
    browserCtx  context.Context     // first chromedp.NewContext from allocator
    browserCancel context.CancelFunc
    mode        Mode                // Launch or Connect
    tabs        *tab.Manager
}

// browser.Manager manages multiple browser instances.
type Manager struct {
    mu        sync.RWMutex
    browsers  map[string]*Browser
    activeID  string
}

// tab.Tab represents a single browser tab with its event collectors.
type Tab struct {
    ID          string
    ctx         context.Context
    cancel      context.CancelFunc
    console     *collector.Console
    errors      *collector.JSErrors
    network     *collector.Network
    performance *collector.Performance
}

// tab.Manager manages tabs within a single browser.
type Manager struct {
    mu        sync.RWMutex
    tabs      map[string]*Tab
    activeID  string
    order     []string // for MRU fallback
}

// collector.RingBuffer[T] is a generic bounded buffer.
type RingBuffer[T any] struct {
    mu      sync.Mutex
    entries []T
    maxSize int
}
```

### Lifecycle

1. **Startup**: Create `browser.Manager` (empty — no browser yet). Create MCP `Server`. Register all tools. Run `server.Run(ctx, StdioTransport)`.
2. **First tool call**: If no browser exists, the tool returns an error telling the LLM to call `browser_launch` or `browser_connect`. Same for missing tabs.
3. **Browser creation**: `browser_launch` calls `chromedp.NewExecAllocator` + `chromedp.NewContext`. `browser_connect` calls `chromedp.NewRemoteAllocator` + `chromedp.NewContext`. Both create a `tab.Manager` for the browser.
4. **Tab creation**: `tab.Manager.NewTab()` calls `chromedp.NewContext(browserCtx)`, starts CDP event listeners (`ListenTarget`), creates collectors, returns tab ID.
5. **Tool execution**: Tool handler resolves the target tab (explicit ID → active tab → auto-create), executes chromedp actions on the tab's context, reads from collectors as needed, returns the result.
6. **Shutdown**: Server context is cancelled. All tab contexts cancelled (closing targets). All browser contexts cancelled (killing launched Chromes, disconnecting from connected ones).

### Error Handling

- No browser exists and auto-launch fails: return tool error describing the failure.
- Invalid browser/tab ID: return tool error with the ID and list of valid IDs.
- Selector not found: return tool error with the selector.
- Navigation timeout: return tool error with the URL and timeout duration.
- Chrome crash: detect via context cancellation, return tool error, mark browser as dead.
- All errors are returned as MCP tool errors (`IsError: true`), not protocol errors, so the LLM can reason about them and retry.

### Concurrency

- Each tab has its own `chromedp.Context` and collectors. Tools targeting different tabs can run concurrently.
- Collectors use `sync.Mutex` for thread-safe access.
- `tab.Manager` and `browser.Manager` use `sync.RWMutex` for concurrent lookups with exclusive creation/deletion.
- chromedp serializes actions on a single target, so concurrent tool calls on the same tab are safe (they queue).

## Tool Annotations

All tools include MCP `ToolAnnotations` for client-side behavior hints:

| Tool Category | `readOnlyHint` | `destructiveHint` | `idempotentHint` |
|---------------|----------------|-------------------|------------------|
| Browser (`browser_launch`, `browser_connect`) | false | false | false |
| Browser (`browser_list`) | true | false | true |
| Browser (`browser_list_profiles`) | true | false | true |
| Browser (`browser_close`) | false | true | false |
| Tab (`tab_new`) | false | false | false |
| Tab (`tab_list`) | true | false | true |
| Tab (`tab_activate`) | false | false | true |
| Tab (`tab_close`) | false | true | false |
| Navigation (`navigate`, `reload`) | false | false | false |
| History (`go_back`, `go_forward`) | false | false | false |
| Waiting (`wait_for`) | true | false | true |
| Visual (`screenshot`, `pdf`) | true | false | true |
| Visual (`set_viewport`) | false | false | true |
| DOM Inspection (`query`, `get_html`, `get_text`, `get_accessibility_tree`) | true | false | true |
| Console/Errors (drain mode) | false | false | false |
| Console/Errors (peek mode) | true | false | true |
| Console (`clear_console`) | false | false | true |
| Network (drain mode) | false | false | false |
| Network (peek mode) | true | false | true |
| Network (`get_response_body`) | true | false | true |
| JS Execution (`evaluate`) | false | false | false |
| Interaction (`click`, `type`, etc.) | false | false | false |
| Cookies (`get_cookies`) | true | false | true |
| Cookies (`set_cookie`) | false | false | true |
| Cookies (`delete_cookies`) | false | true | false |
| Performance (`get_performance_metrics`) | true | false | true |
| Performance (`get_layout_shifts` drain) | false | false | false |
| Performance (`get_coverage`) | true | false | true |
| Emulation (`set_geolocation`, `set_timezone`, `set_locale`, `set_user_agent`, `set_cpu_throttling`, `set_vision_deficiency`) | false | false | true |
| Network (`emulate_network`, `block_urls`) | false | false | true |

## Configuration

All browser configuration is done via tools at runtime. The server accepts optional flags:

```
chromedp-mcp [--download-dir <path>] [--allowed-profiles <names>]
```

| Flag | Description |
|------|-------------|
| `--download-dir` | Directory for saving screenshots, PDFs, and downloads. When set, `screenshot` and `pdf` tools accept a `filename` parameter to save output to disk, and Chrome downloads are enabled with automatic file saving and event tracking via `get_downloads`. Path traversal is blocked — filenames must not contain directory separators. The directory is created automatically if it doesn't exist. |
| `--allowed-profiles` | Comma-separated list of Chrome profile display names the LLM may use (e.g. `"Work,Personal"`). When set, registers the `browser_list_profiles` tool and enables the `profile` parameter on `browser_launch`. Profiles not in this list are never exposed to the LLM. The Chrome/Chromium user data directory is auto-detected from platform defaults. |

When `--download-dir` is not set, the tools return binary data inline only and requesting a `filename` returns an error.

### User Profile Security

> **Warning:** Enabling `--allowed-profiles` gives the LLM access to real Chrome profiles, including saved cookies, active sessions, localStorage, and extension data. The LLM can act as the user on any site they are logged into in that profile. Only expose profiles you understand the implications of, and never expose profiles containing sensitive credentials to untrusted LLM providers.

Profile access is disabled by default. The `--allowed-profiles` flag is the explicit opt-in gate. When not set, the `browser_list_profiles` tool is not registered and the `profile` parameter on `browser_launch` is rejected. Each launched browser gets a fresh temporary profile that is discarded on close.

Profile discovery works by reading Chrome's `Local State` JSON file, which maps profile directory names (`Default`, `Profile 1`, `Profile 2`, etc.) to user-chosen display names. The `--allowed-profiles` flag accepts display names, and only matching profiles are returned by `browser_list_profiles` or accepted by `browser_launch`.

Note that Chrome enforces a singleton lock on user data directories. Only one Chrome process can use a given user data directory at a time. If the user's Chrome is already running, launching a browser with a profile from the same user data directory will fail.

## Future Features

Features not in the initial scope but supported by CDP and worth adding later.

### Storage

- **Local/Session Storage** — Read, write, delete, clear DOM storage items via `domstorage` domain. Useful for debugging state persistence issues.
- **IndexedDB** — List databases, inspect object stores, read/query data, delete entries via `indexeddb` domain. Useful for debugging offline-first apps.
- **Cache Storage** — List caches, inspect entries, read cached responses, delete caches via `cachestorage` domain. Useful for debugging service worker caching strategies.

### Network Control

- **Request Interception** — Intercept, modify, mock, or fail network requests via `fetch` domain. Enables testing error states, simulating API responses, and injecting faults.
- **Certificate Inspection** — Get site certificate details via `network.GetCertificate`.

### Emulation

- **Device Emulation** — Emulate specific devices (iPhone, Pixel, etc.) with appropriate viewport, user agent, touch, and device scale factor.

### Security & Auth

- **WebAuthn** — Create virtual authenticators, add/manage credentials, simulate user verification via `webauthn` domain. Enables testing passkey and FIDO2 flows without physical hardware.

### Media

- **Media Inspection** — Monitor audio/video playback, codecs, errors, and buffering via `media` domain. Useful for debugging streaming and video player issues.
- **Web Audio** — Inspect Web Audio graph and real-time processing data via `webaudio` domain.

### Debugging

- **CSS Manipulation** — Add CSS rules, modify styles, force pseudo-states (`:hover`, `:focus`, `:active`) via `css` domain. Useful for visually inspecting and debugging styles.
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

- **Drag and Drop** — Simulate drag-and-drop interactions via `input.DispatchDragEvent`.
- **Touch Gestures** — Simulate multi-touch, pinch, and swipe gestures via `input.DispatchTouchEvent` and `input.SynthesizePinchGesture` / `SynthesizeScrollGesture`.
- **Animations** — Inspect and control CSS animations via `animation` domain. Pause, seek, change playback rate.
- **Layer Tree** — Inspect compositing layers for debugging rendering performance and GPU usage via `layertree` domain.
