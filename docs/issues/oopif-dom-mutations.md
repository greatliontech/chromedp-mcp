# DOM mutations inside out-of-process iframes are not counted

**Lands:** When cross-origin iframe DOM inspection is required by a tool, or when the server attaches to OOPIF targets for any other reason.

## The gap

Activity observation (`observe_window_ms` on the action tools) counts DOM mutations per frame, by evaluating in each frame's main-world JavaScript execution context. That works for every frame in the page's process.

A **cross-origin iframe is out-of-process**: Chrome gives it its own renderer and its own CDP session. This server attaches only to the page target, and chromedp drops messages from sessions it did not attach, so the frame's `Runtime.executionContextCreated` never reaches us. We hold no execution context for it and cannot read the mutation buffer the observer script maintains inside it.

This affects the common case, not an exotic one: third-party ads, embeds, payment frames, OAuth frames.

## What happens today

The frame is not invisible. It announces itself with `Page.frameAttached` *before* it swaps into its own process, and it then leaves our session with `Page.frameDetached` reason `swap` — which is deliberately **not** treated as removal, because that event is our only evidence the frame exists. (`Page.getFrameTree` does not list out-of-process frames at all, so the event stream is the sole source.) The server therefore knows such a frame exists and knows it cannot see inside it.

`dom_mutations` therefore reports what it could see and sets **`dom_mutations_partial: true`**, meaning "this is a lower bound". It never reports a confident `0` for mutations it simply could not observe.

The other observation signals are unaffected: `network_requests`, `console_messages`, `js_errors`, and `url_changed` are not frame-scoped and do cover out-of-process frames.

## What full support would take

Attach to the OOPIF targets themselves: enable `Target.setAutoAttach` with the iframe targets registered as sessions we listen on, install the mutation observer in each, and route their `Runtime` events into the tab's `ExecContexts`. chromedp does not expose session registration for auto-attached child targets, so this needs either direct CDP session handling alongside chromedp or an upstream change.

Until then, `dom_mutations_partial` makes the limit legible to the caller rather than silent.
