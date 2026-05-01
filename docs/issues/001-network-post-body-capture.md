# `get_network_requests` does not include request bodies

## Summary

`get_network_requests` returns URL, method, headers, status, response headers,
and timing — but not the **request body** for `POST`/`PUT`/`PATCH`/`DELETE`.
This is the single biggest gap when using the MCP for reverse-engineering an
SPA: the LLM can see *that* a POST happened and what came back, but not what
was sent.

## Reproduction

1. Launch a browser, navigate to any site that issues a non-trivial POST
   (e.g. `https://wolt.com/en/cyp/nicosia/restaurant/<slug>`, click an item,
   add to basket).
2. Call `get_network_requests` filtered to that endpoint.
3. Inspect the returned object.

```jsonc
{
  "method": "POST",
  "url": "https://consumer-api.wolt.com/order-xp/v1/baskets",
  "request_headers": {...},      // present
  "response_headers": {...},     // present
  // no body field at all
  "status": 200,
  "size": 405                    // response size
}
```

The CDP event `Network.requestWillBeSent` carries `request.postData` (and
`Network.getRequestPostData` is available for cases where it was streamed).
Neither surfaces in the MCP tool result.

## Current behavior

The body is silently absent. There is no error, no flag to opt in, no
companion tool to fetch it after the fact.

## Expected behavior

One of:

- Add `request_body` to the returned request object whenever the body is
  available (string for text/JSON, base64 for binary).
- Add a sibling tool `get_request_body(request_id)` that calls
  `Network.getRequestPostData` on demand. Avoids bloating the default
  response.
- Document the gap and the in-page interceptor workaround prominently in
  the README.

## Workaround used

Inject a JS interceptor on each new document via `add_script` that wraps
`window.fetch` and `XMLHttpRequest.prototype.send`, recording bodies into
`window.__captured`. Then read via `evaluate`. Two caveats:

- Anything fired *before* the interceptor installs is lost (cookies were
  already captured by the SPA's own first XHR by the time we hooked in).
- An identical patch must be installed via `evaluate` on the **active
  document** in addition to `add_script` (which only fires on new
  documents). See issue 006.

## Notes

This is the highest-leverage fix for any reverse-engineering workflow.
Without it, the LLM has to write a JS interceptor as the first step of every
session, which is fragile and incomplete.
