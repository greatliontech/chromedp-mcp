# chromedp-mcp

An MCP server that gives LLMs a fully-featured headless browser via Chrome DevTools Protocol.

```
                                                      CDP (WebSocket)
              MCP (stdio)         +----------------+  ────────────────>  +---------+
+-------------+ <──────────────>  |  chromedp-mcp  |                    | Chrome  |
|  LLM / Host |   tools           |                |  <──────────────── | Browser |
+-------------+                   |  - browser mgr |  events, results   +---------+
                                  |  - tab mgr     |
                                  |  - collectors  |
                                  +----------------+
```

LLMs can launch or connect to Chrome browsers, manage tabs (including automatically adopted popups), inspect pages with compact accessibility snapshots (`[ref=e1]`), pierce open Shadow DOM (`>>>`) and same-origin iframes, interact with elements or viewport coordinates, capture screenshots/PDFs, intercept and inspect network/WebSocket traffic, read console logs and performance metrics, and emulate devices/environments — all through standard MCP tool calls.

## Requirements

- Go 1.27+
- Chrome or Chromium installed and available in `$PATH`

## Installation

```sh
go install github.com/greatliontech/chromedp-mcp/cmd/chromedp-mcp@latest
```

## Usage

chromedp-mcp communicates over stdio, the standard transport for MCP servers integrated with LLM clients.

```sh
chromedp-mcp [--download-dir <path>] [--allowed-profiles <names>] [--user-data-dir <path>]
```

| Flag                 | Description                                                                                                                                                                                                                                   |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--download-dir`     | Directory for saving screenshots, PDFs, and file downloads (supports `~` expansion). When set, registers `get_downloads`, enables Chrome file downloads with automatic tracking/renaming, and enables atomic `filename` saving on `screenshot` and `pdf`. |
| `--allowed-profiles` | Comma-separated list of Chrome profile display names the LLM may use (e.g. `"Work,Personal"`). Registers `browser_list_profiles` and exposes the `profile` parameter on `browser_launch`. See [User Profiles](#user-profiles) for details.    |
| `--user-data-dir`    | Override the Chrome/Chromium user data directory used for profile discovery (supports `~` expansion). When omitted, the default platform location is auto-detected (e.g. `~/.config/google-chrome` on Linux). Only relevant when `--allowed-profiles` is set. |

### Claude Desktop

Add to your Claude Desktop config (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "browser": {
      "command": "chromedp-mcp",
      "args": ["--download-dir", "~/Downloads"]
    }
  }
}
```

### Claude Code

```sh
claude mcp add browser chromedp-mcp -- --download-dir ~/Downloads
```

### OpenCode

Add to your `opencode.json` (typically `~/.config/opencode/opencode.json`):

```json
{
  "mcp": {
    "browser": {
      "type": "local",
      "command": ["chromedp-mcp", "--download-dir", "~/Downloads"]
    }
  }
}
```

### Other MCP clients

Any MCP client that supports stdio transport can use chromedp-mcp. Point it at the `chromedp-mcp` binary with optional flags.

## User Profiles

> **Security Warning:** Enabling user profiles gives the LLM access to a real Chrome profile, including saved cookies, active sessions, localStorage, and extension data. The LLM can act as you on any site you are logged into in that profile. Only enable profiles you understand the implications of, and never expose profiles containing sensitive credentials to untrusted LLM providers.

By default, chromedp-mcp launches browsers with ephemeral temporary profiles — no user data persists after the browser is closed, and no existing profile data is accessible. This is the safe default.

The `--allowed-profiles` flag opts in to profile access. When set, the LLM gains two capabilities:

1. **`browser_list_profiles`** — Discover which profiles are available (only those in the allowed list).
2. **`browser_launch` with `profile`** — Launch a browser using a real Chrome profile by its display name. When `--allowed-profiles` is not set, `profile` is omitted from `browser_launch`'s schema and rejected if passed.

### How it works

Chrome stores user profiles in a user data directory (e.g. `~/.config/google-chrome` on Linux). Each profile has a directory (`Default`, `Profile 1`, `Profile 2`, etc.) and a user-chosen display name (e.g. `"Work"`, `"Personal"`). The `--allowed-profiles` flag accepts display names.

The server reads Chrome's `Local State` file to map display names to profile directories. Only profiles explicitly listed in `--allowed-profiles` are exposed to the LLM. By default, the server auto-detects the platform's Chrome user data directory. Use `--user-data-dir` to override this (e.g. to point at a Chromium installation or a custom Chrome data directory).

### Recommendations

- **Create dedicated profiles.** Don't expose your primary browser profile. Create a separate profile in Chrome specifically for LLM use, with only the accounts and extensions needed for the task.
- **Limit the allowed list.** Only include the profiles the LLM actually needs. One profile per use case is ideal.
- **Close Chrome first.** Chrome enforces a singleton lock on user data directories. You must close all Chrome instances before the MCP server can launch a browser with a real profile. Alternatively, use a separate Chrome installation or user data directory.
- **Be aware of what's accessible.** A profile may contain saved passwords, autofill data, cookies for banking/email/social media, and browser extensions with their own permissions. The LLM can access anything the profile can.

## Tools

chromedp-mcp exposes **41 tools by default**, plus 2 conditionally registered tools (`get_downloads` when `--download-dir` is set, and `browser_list_profiles` when `--allowed-profiles` is set; **43 tools max**). The browser lifecycle is entirely tool-driven — the LLM decides when to launch browsers and manage tabs.

### Browser Management

| Tool                    | Description                                                                                                  |
| ----------------------- | ------------------------------------------------------------------------------------------------------------ |
| `browser_launch`        | Launch a new Chrome instance (headless by default) and attach its default tab, optionally with a user profile |
| `browser_connect`       | Connect to a running Chrome via remote debugging URL and adopt its open tabs                                 |
| `browser_activate`      | Set a managed browser as the active browser                                                                  |
| `browser_close`         | Close a browser (kills process or disconnects, preserving adopted user tabs)                                 |
| `browser_list`          | List all managed browsers                                                                                    |
| `browser_list_profiles` | List available user profiles (registered only when `--allowed-profiles` is set)                              |

### Tab Management

| Tool           | Description                                                           |
| -------------- | --------------------------------------------------------------------- |
| `tab_new`      | Create a new tab, optionally navigating to a URL                      |
| `tab_list`     | List all open tabs with IDs, URLs, and titles (including adopted popups) |
| `tab_activate` | Set a tab as the active tab and bring it to the front                 |
| `tab_close`    | Close a tab                                                           |

### Navigation

| Tool       | Description                                                                                                              |
| ---------- | ------------------------------------------------------------------------------------------------------------------------ |
| `navigate` | Navigate a tab (`goto` with `load`/`domcontentloaded`/`networkidle`, `reload` with optional `bypass_cache`, `back`, `forward`) |
| `wait_for` | Wait for a selector state (`visible`/`hidden`/`attached`/`detached`), visible text, tab URL substring, or JS expression  |

### Visual Feedback

| Tool         | Description                                                                                           |
| ------------ | ----------------------------------------------------------------------------------------------------- |
| `screenshot` | Capture viewport, full page, or element screenshots (`ref` or CSS/`>>>` selector + optional `frame`, PNG/JPEG) |
| `pdf`        | Generate a PDF of the current page (inline blob or atomic save to `--download-dir`)                   |

### DOM & Accessibility Inspection

| Tool          | Description                                                                                                       |
| ------------- | ----------------------------------------------------------------------------------------------------------------- |
| `snapshot`    | Capture a compact accessibility snapshot with short `[ref=e1]` element refs (pierces open Shadow DOM, bounded by `max_bytes`) |
| `query`       | Query elements by CSS/`>>>` shadow selector (with optional `frame`) returning attributes, text, styles, and bounding boxes |
| `get_dom`     | Get visible/full `text` or outer/inner `html` of a page, frame, or element (`ref` or CSS/`>>>` selector), bounded by `max_bytes` |
| `list_frames` | List all frames and same-origin/OOPIF reachability for the optional `frame` parameter                             |

### Interaction

All element-targeting action tools accept either a snapshot `ref` (e.g. `"e4"`), a CSS/`>>>` shadow selector (with optional same-origin `frame`), or viewport `(x, y)` coordinates where applicable (`click`, `hover`, `drag_and_drop`). `click`, `type`, `select_option`, `press_key`, and `drag_and_drop` interrupt promptly if a synchronous JavaScript modal dialog opens during dispatch instead of hanging.

| Tool            | Description                                                                                        |
| --------------- | -------------------------------------------------------------------------------------------------- |
| `click`         | Click an element (`ref`/CSS/`>>>`/frame) or `(x, y)` with button, click count, and modifiers       |
| `type`          | Type text into an input (`replace` or `append` mode), with optional `delay` and `submit` (Enter)   |
| `select_option` | Select option(s) on a `<select>` by `values`, `labels`, or `indexes` (pass `[]` to deselect multi) |
| `scroll`        | Scroll an element into view (`ref`/selector/frame) or scroll the window/frame by `(x, y)` pixels   |
| `hover`         | Hover over an element (`ref`/CSS/`>>>`/frame) or viewport `(x, y)` coordinates                     |
| `press_key`     | Press a keyboard key with optional modifiers, optionally focusing a target element first           |
| `upload_files`  | Set one or more file paths on a file input (`ref`/CSS/`>>>`/frame)                                 |
| `drag_and_drop` | Drag source onto target (`ref`, CSS/`>>>`/frame, or `(x, y)` coords; supports HTML5 & pointer drag) |
| `dialog`        | Inspect (`get`) or handle (`accept`/`dismiss`, with optional prompt `text`) JS modal dialogs       |

### JavaScript

| Tool       | Description                                                                                                     |
| ---------- | --------------------------------------------------------------------------------------------------------------- |
| `evaluate` | Execute JavaScript in the page or a same-origin frame, optionally scoped to an element (`ref` or CSS/`>>>` selector), with optional `max_result_bytes` cap |

### Console & Buffers

| Tool               | Description                                                                                    |
| ------------------ | ---------------------------------------------------------------------------------------------- |
| `get_console_logs` | Get captured console messages (`log`, `warn`, `error`, `info`, `debug`, `assert`, `trace`, etc.) |
| `get_js_errors`    | Get captured JavaScript exceptions and unhandled promise rejections                            |
| `clear_buffers`    | Clear selected or all event collector buffers while preserving live/in-flight state            |

### Network, Interception & Downloads

| Tool                   | Description                                                                                        |
| ---------------------- | -------------------------------------------------------------------------------------------------- |
| `get_network_requests` | Get captured HTTP requests and WebSocket connections with filtering (`type`, status, URL) and `fields` projection |
| `get_network_body`     | Get a `response` (default) or `request` body by `request_id` with byte-range slicing               |
| `get_websocket_frames` | Get sent/received WebSocket frames for a connection                                                |
| `intercept`            | Manage Fetch interception rules (`fulfill`, `modify`, `fail`, `remove`, `list`, `clear`)           |
| `get_downloads`        | Get tracked file downloads with status and paths (registered only when `--download-dir` is set)    |

### Cookies & Storage

| Tool            | Description                                                                                      |
| --------------- | ------------------------------------------------------------------------------------------------ |
| `cookies`       | Get (bounded by `limit`), set, or delete browser cookies                                         |
| `storage`       | Read (`key`, `keys_only`, or bounded all-entries), write, or delete `localStorage`/`sessionStorage` |
| `indexeddb`     | Inspect (`get`: list DBs, stores, paginated records) or `delete` IndexedDB databases/stores      |
| `cache_storage` | Inspect (`get`: list caches, entries, range-sliced bodies) or `delete` Cache Storage caches/entries |

### Performance, Emulation & Page Configuration

| Tool              | Description                                                                                             |
| ----------------- | ------------------------------------------------------------------------------------------------------- |
| `get_performance` | Get runtime metrics, layout shifts with W3C session-window CLS, and Largest Contentful Paint (LCP)      |
| `emulate`         | Emulate `viewport`, `device` presets, `network`, `geolocation`, `timezone`, `locale`, `user_agent`, `cpu_throttling`, `vision_deficiency`, `media`, or `pseudo_state` |
| `configure_page`  | Manage new-document scripts (`add_script`/`remove_script`), custom `set_headers`, `set_permission`, and `set_ignore_cert_errors` |

### Observation

Observation is not a separate tool — it is an `observe_window_ms` parameter on all nine action tools (`click`, `type`, `select_option`, `scroll`, `hover`, `press_key`, `upload_files`, `drag_and_drop`, and `dialog` on `accept`/`dismiss`).

Pass it a value in milliseconds (max 30000) and the tool measures what the page actually did in response to the action, returning an `observed` object alongside its normal result:

```json
{
  "window_ms": 512,
  "network_requests": 2,
  "dom_mutations": 7,
  "url_changed": false,
  "console_messages": 0,
  "js_errors": 0,
  "has_any_effect": true
}
```

This catches the **silent no-op** — an action that dispatches cleanly and reports success while the page ignores it (a click whose handler early-returns, a form submit swallowed by a validation guard). `has_any_effect: false` says the action landed but nothing happened, which is otherwise invisible to the caller.

Measuring inline rather than in a follow-up call is what makes it reliable: the window opens inside the handler — after the tool has waited for its target element, immediately before dispatch — so there is no gap for effects to escape through while an LLM decides what to call next, and no credit taken for activity that happened while the page was still producing the element.

Three flags mark a measurement the caller must not over-read, all omitted when false:

- `truncated` — the tab closed or the client disconnected before the window elapsed, so the counts are partial and a false `has_any_effect` proves nothing.
- `dom_mutations_unavailable` — the top-frame document was replaced mid-window or a JS modal dialog blocked renderer evaluation, so `dom_mutations` carries no information; use `url_changed` or `dialog(action="get")`.
- `dom_mutations_partial` — `dom_mutations` is a lower bound. Some frame could not be seen (e.g. a cross-origin iframe runs out-of-process on a CDP session we are not attached to, or a subframe was created/replaced mid-window). A zero here does not mean the DOM didn't change.

For full parameter documentation, see [docs/design.md](docs/design.md).

## How It Works

- **Explicit browser and tab lifecycle.** If no browser is running when a tool is called, an error tells the LLM to call `browser_launch` or `browser_connect` first. `browser_launch` attaches Chrome's initial default tab (and `browser_connect` adopts existing open tabs) so `navigate` works immediately; popups and `target="_blank"` windows are adopted automatically in the background without stealing the active tab. If all tabs are closed, tools ask for `tab_new`.
- **Snapshot refs, Shadow DOM, and iframes.** `snapshot` renders a compact accessibility outline with short `[ref=eN]` handles usable directly across interaction, visual, DOM, JS, and pseudo-state tools. Refs pierce open Shadow DOM automatically and invalidate when their document navigates. CSS selectors also support `>>>` to pierce nested open shadow roots, and `frame` targets any reachable same-origin iframe from `list_frames`.
- **Multiple browsers and tabs.** The most recently used browser/tab is the active one. Tools default to the active browser and tab unless an ID is specified.
- **Background event collectors.** Console logs, JS errors, network requests, WebSocket frames, and performance timeline data are captured automatically per tab in bounded ring buffers, available on demand via `peek` (read only) or `drain` (read + consume) modes.
- **MCP request cancellation.** All tool handlers respect client disconnection — if the MCP client drops the connection, in-flight CDP operations terminate immediately.

## Security

This MCP gives the connected LLM a real Chrome session — including its cookies, `Authorization` headers, `localStorage`, and any logged-in state. Tools return that data verbatim. There is no built-in redaction. Plan deployments accordingly.

**Threat model.** Anything the browser session can see, the LLM can see, and anything the LLM sees can land in:

- the conversation transcript (typically stored by the MCP host),
- spilled tool-result files written to disk by the host when responses exceed token budgets (e.g. `~/.claude/projects/.../tool-results/*.txt`),
- any host-side telemetry or logging the operator has enabled.

Concretely:

- `cookies(action="get")` returns cookie values verbatim.
- `get_network_requests` returns request and response headers verbatim, including `Authorization` and `Cookie`.
- `get_network_body` returns raw response and request payloads, which routinely contain bearer tokens and refresh tokens.
- `configure_page(action="set_headers")` accepts auth tokens that are then sent on every request from the tab.

**Recommended deployment.**

- Use a dedicated Chrome user data directory for MCP-driven sessions. Don't point `--user-data-dir` (or the default profile path) at the same profile you use for personal browsing.
- Don't allowlist (`--allowed-profiles`) profiles that are signed into high-value accounts (banking, primary email, password managers).
- Treat the MCP host's transcript and any spill files as containing whatever credentials the browser session held during the conversation.
- For workflows that must touch a logged-in production account, prefer short-lived test credentials over your own session cookies.

If your deployment can't accept this posture (e.g. multi-tenant LLM service, untrusted operator), don't run chromedp-mcp against a sensitive browser session — the tool surface is not designed to sanitize output.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
