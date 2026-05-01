# `add_script` doesn't apply to the active document

## Summary

`add_script` registers JS to run on every **new** document via CDP
`Page.addScriptToEvaluateOnNewDocument`. It correctly survives navigations,
but it does not execute on the document that's already loaded when the tool
is called. The LLM has to install the same script a second time via
`evaluate` to cover the live page, which is easy to forget.

## Reproduction

1. Load a page.
2. `add_script({source: "window.__pingedAt = Date.now();"})`.
3. `evaluate({expression: "window.__pingedAt || 'not-set'"})` → returns
   `"not-set"`.
4. Navigate (or reload).
5. `evaluate(...)` → returns a real timestamp.

## Current behavior

`add_script` is for future documents only. This is faithful to CDP semantics
but a footgun for any "instrument the current page" use case.

## Expected behavior

One of:

- Add a parameter `apply_to_current: bool` (default `true`) that also
  evaluates the script in the current document.
- Document the limitation in the tool description with the explicit advice
  "if you also want this to run on the current page, follow with an
  `evaluate` of the same source."
- Provide a sibling tool `inject_script` that does both: install on
  new-documents AND eval-on-current.

## Workaround used

Always pair `add_script` with an `evaluate` of the same source IIFE, e.g.:

```js
add_script({source: "(() => { ...interceptor... })();"})
evaluate({expression: "(() => { ...interceptor... })();"})
```

Tedious and error-prone for non-trivial scripts (have to keep the two in
sync).

## Notes

Related: when the same script wraps `window.fetch` or `XMLHttpRequest`,
double-installation must be idempotent — easy to handle with a
`window.__installed` guard, but worth calling out in docs.
