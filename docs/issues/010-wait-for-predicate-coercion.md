# `wait_for` errors `Object reference chain is too long` on complex predicates

## Summary

`wait_for({expression: "..."})` fails with CDP error code `-32000` and
message `Object reference chain is too long` when the predicate evaluates
to a non-primitive (object, array, DOM node, complex chain). The LLM only
needs a truthy/falsy signal — never the actual return value — so coercing
to a boolean server-side would prevent the error entirely.

## Reproduction

```js
wait_for({
  expression: "document.querySelector('[role=\"dialog\"]')?.innerText?.toLowerCase()?.includes('apartment')",
  timeout: 5000
})
```

Fails with:

```
Object reference chain is too long (-32000)
```

The same predicate wrapped in `Boolean(...)` succeeds:

```js
wait_for({
  expression: "Boolean(document.querySelector('[role=\"dialog\"]')?.innerText?.toLowerCase()?.includes('apartment'))",
  timeout: 5000
})
```

Saw this twice in a single session driving Wolt's address-add modal.

## Current behavior

CDP `Runtime.evaluate` is invoked with the LLM's expression as-is. When the
expression returns a complex chained value (optional-chains over DOM nodes,
or anything that evaluates to a wrapper object), CDP's serialization layer
balks. The error bubbles up as the tool result.

## Expected behavior

Two non-exclusive options:

- **Server-side coercion**: wrap the LLM's expression in `Boolean(...)` (or
  `!!(...)`) inside the chromedp-mcp tool implementation before sending to
  CDP. Predicates only ever need truthy semantics, so the coercion is
  always safe and removes a sharp edge entirely. Cost: one string concat in
  Go.
- **Friendly error message**: catch the specific CDP error code and
  rewrite the message to "wait_for predicate returned a non-primitive;
  wrap with `Boolean(...)` or return a primitive value. Original error:
  ...". Helps the LLM self-correct on the next turn.

The first is preferable — it eliminates the failure mode rather than
documenting it.

## Workaround used

Rewrite the predicate to return a primitive: either wrap the whole thing in
`Boolean(...)`, use `!!(...)`, or restructure to compare against a string
or count (e.g. `document.querySelectorAll('foo').length > 0`).

## Notes

`evaluate` itself accepts complex return values just fine because it has
`returnByValue: true`-style serialization that handles them. The
discrepancy is that `wait_for` evaluates differently (it's a polling loop
that just needs truthiness). Aligning the two behaviors with a server-side
coercion in `wait_for` is the smallest possible fix.
