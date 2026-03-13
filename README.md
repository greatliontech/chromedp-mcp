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

LLMs can launch or connect to Chrome browsers, open tabs, navigate pages, interact with elements, capture screenshots, inspect network traffic, read console logs, and much more — all through standard MCP tool calls.

## Requirements

- Go 1.21+
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

| Flag                 | Description                                                                                                                                                                                                                                     |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--download-dir`     | Directory for saving screenshots, PDFs, and file downloads. When set, the `screenshot` and `pdf` tools accept a `filename` parameter to save output to disk, and Chrome downloads are enabled with automatic file tracking via `get_downloads`. |
| `--allowed-profiles` | Comma-separated list of Chrome profile display names the LLM may use (e.g. `"Work,Personal"`). Enables the `browser_list_profiles` tool and the `profile` parameter on `browser_launch`. See [User Profiles](#user-profiles) for details. |
| `--user-data-dir`    | Override the Chrome/Chromium user data directory used for profile discovery. When omitted, the default platform location is auto-detected (e.g. `~/.config/google-chrome` on Linux). Only relevant when `--allowed-profiles` is set. |

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
2. **`browser_launch` with `profile`** — Launch a browser using a real Chrome profile by its display name.

### How it works

Chrome stores user profiles in a user data directory (e.g. `~/.config/google-chrome` on Linux). Each profile has a directory (`Default`, `Profile 1`, `Profile 2`, etc.) and a user-chosen display name (e.g. "Work", "Personal"). The `--allowed-profiles` flag accepts display names.

The server reads Chrome's `Local State` file to map display names to profile directories. Only profiles explicitly listed in `--allowed-profiles` are exposed to the LLM. By default, the server auto-detects the platform's Chrome user data directory. Use `--user-data-dir` to override this (e.g. to point at a Chromium installation or a custom Chrome data directory).

### Recommendations

- **Create dedicated profiles.** Don't expose your primary browser profile. Create a separate profile in Chrome specifically for LLM use, with only the accounts and extensions needed for the task.
- **Limit the allowed list.** Only include the profiles the LLM actually needs. One profile per use case is ideal.
- **Close Chrome first.** Chrome enforces a singleton lock on user data directories. You must close all Chrome instances before the MCP server can launch a browser with a real profile. Alternatively, use a separate Chrome installation or user data directory.
- **Be aware of what's accessible.** A profile may contain saved passwords, autofill data, cookies for banking/email/social media, and browser extensions with their own permissions. The LLM can access anything the profile can.

## Tools

chromedp-mcp exposes 40+ tools organized by category. The browser lifecycle is entirely tool-driven — the LLM decides when to launch browsers and create tabs.

### Browser Management

| Tool                     | Description                                                                         |
| ------------------------ | ----------------------------------------------------------------------------------- |
| `browser_launch`         | Launch a new Chrome instance (headless by default), optionally with a user profile  |
| `browser_connect`        | Connect to a running Chrome via remote debugging URL                                |
| `browser_close`          | Close a browser (kills process or disconnects)                                      |
| `browser_list`           | List all managed browsers                                                           |
| `browser_list_profiles`  | List available user profiles (requires `--allowed-profiles`)                        |

### Tab Management

| Tool           | Description                                      |
| -------------- | ------------------------------------------------ |
| `tab_new`      | Create a new tab, optionally navigating to a URL |
| `tab_list`     | List all open tabs with URLs and titles          |
| `tab_activate` | Set a tab as the active tab                      |
| `tab_close`    | Close a tab                                      |

### Navigation

| Tool                     | Description                                                                          |
| ------------------------ | ------------------------------------------------------------------------------------ |
| `navigate`               | Navigate to a URL with optional wait condition (load, domcontentloaded, networkidle) |
| `reload`                 | Reload the current page                                                              |
| `go_back` / `go_forward` | Navigate browser history                                                             |
| `wait_for`               | Wait for a CSS selector or JS expression                                             |

### Visual Feedback

| Tool           | Description                                                    |
| -------------- | -------------------------------------------------------------- |
| `screenshot`   | Capture viewport, full page, or element screenshots (PNG/JPEG) |
| `pdf`          | Generate a PDF of the current page                             |
| `set_viewport` | Set viewport dimensions and device scale factor                |

### DOM Inspection

| Tool                     | Description                                                                  |
| ------------------------ | ---------------------------------------------------------------------------- |
| `query`                  | Query elements by CSS selector with attributes, text, styles, bounding boxes |
| `get_html`               | Get inner/outer HTML of the page or a subtree                                |
| `get_text`               | Get visible text content                                                     |
| `get_accessibility_tree` | Get the accessibility tree (token-efficient page structure)                  |

### Interaction

| Tool            | Description                                         |
| --------------- | --------------------------------------------------- |
| `click`         | Click an element (left/right/middle, single/double) |
| `type`          | Type text into an input element                     |
| `select_option` | Select an option from a `<select>` element          |
| `submit_form`   | Submit a form                                       |
| `scroll`        | Scroll the page or an element into view             |
| `hover`         | Hover over an element                               |
| `focus`         | Focus an element                                    |
| `press_key`     | Press a keyboard key with optional modifiers        |
| `upload_files`  | Set files on a file input                           |
| `handle_dialog` | Accept or dismiss JavaScript dialogs                |

### JavaScript

| Tool       | Description                                                             |
| ---------- | ----------------------------------------------------------------------- |
| `evaluate` | Execute JavaScript in the page context, optionally scoped to an element |

### Console & Errors

| Tool               | Description                                                   |
| ------------------ | ------------------------------------------------------------- |
| `get_console_logs` | Get captured console messages (log, warn, error, info, debug) |
| `get_js_errors`    | Get captured JavaScript exceptions                            |
| `clear_console`    | Clear console and error buffers                               |

### Network

| Tool                   | Description                                         |
| ---------------------- | --------------------------------------------------- |
| `get_network_requests` | Get captured network requests with filtering        |
| `get_response_body`    | Get the response body of a specific request         |
| `get_downloads`        | Get tracked file downloads with status and progress |

### Cookies

| Tool             | Description                         |
| ---------------- | ----------------------------------- |
| `get_cookies`    | Get browser cookies                 |
| `set_cookie`     | Set a browser cookie                |
| `delete_cookies` | Delete cookies by name or clear all |

### Storage

| Tool               | Description                                              |
| ------------------ | -------------------------------------------------------- |
| `get_storage`      | Read one or all items from localStorage or sessionStorage |
| `set_storage`      | Write a key-value pair to localStorage or sessionStorage  |
| `delete_storage`   | Delete one or all items from localStorage or sessionStorage |
| `get_storage_keys` | List all keys with value sizes                            |

### Performance

| Tool                      | Description                                              |
| ------------------------- | -------------------------------------------------------- |
| `get_performance_metrics` | Get runtime metrics (heap, DOM nodes, layout, scripting) |
| `get_layout_shifts`       | Get Cumulative Layout Shift (CLS) data                   |
| `get_coverage`            | Get CSS/JS code coverage data                            |

### Configuration & Emulation

| Tool                            | Description                                                   |
| ------------------------------- | ------------------------------------------------------------- |
| `add_script` / `remove_script`  | Inject JavaScript to run on every new document                |
| `set_extra_headers`             | Inject custom HTTP headers into all requests                  |
| `set_permission`                | Grant, deny, or reset browser permissions                     |
| `set_emulated_media`            | Override CSS media type and features (dark mode, print, etc.) |
| `set_ignore_certificate_errors` | Ignore TLS certificate errors                                 |
| `set_geolocation`               | Override device geolocation                                   |
| `set_timezone`                  | Override browser timezone                                     |
| `set_locale`                    | Override browser locale                                       |
| `set_user_agent`                | Override user agent string                                    |
| `set_cpu_throttling`            | Throttle CPU to simulate slow devices                         |
| `set_vision_deficiency`         | Simulate vision deficiencies for accessibility testing        |
| `emulate_network`               | Emulate network conditions (offline, latency, throttling)     |
| `block_urls`                    | Block URLs matching wildcard patterns                         |

For full parameter documentation, see [docs/design.md](docs/design.md).

## How It Works

- **No implicit browser creation.** If no browser is running when a tool is called, an error tells the LLM to call `browser_launch` or `browser_connect` first. Same for tabs.
- **Multiple browsers and tabs.** The most recently used browser/tab is the active one. Tools default to the active browser and tab unless an ID is specified.
- **Background event collectors.** Console logs, JS errors, network requests, and performance data are captured automatically per tab in ring buffers, available on demand via drain (read + clear) or peek (read only) modes.
- **MCP request cancellation.** All tool handlers respect client disconnection — if the MCP client drops the connection, in-flight CDP operations terminate immediately.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
