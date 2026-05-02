# `click` could grow a `wait_for_effect` parameter

## Summary

When issue 005 was resolved, the spec listed three options for surfacing
the "click had no effect" failure mode:

1. Probe target state and return it (implemented).
2. Optional `wait_for_effect` parameter that polls for a network request,
   DOM mutation, or navigation within a short window after dispatch
   (deferred).
3. Hard-error on `disabled` / `aria-disabled="true"` (implemented).

The user picked 1 + 3. Option 2 was deliberately scoped out because
"what counts as an effect" is fuzzy and most cases are caught either by
the pre-click probe (disabled, aria-disabled) or the post-click warning
(pointer-events, visibility), but the long tail it would catch is real.

## Cases option 2 would still catch

After 005 ships:

- `<button>` whose own state is clean but whose click handler short-
  circuits. Example from the original session: Wolt's address-edit form
  Save button is `disabled: false`, has `pointer-events: auto`, and is
  fully visible — but its `onSubmit` handler short-circuits when
  `aria-invalid` is set on a sibling. The click lands, no POST fires,
  the LLM has to discover the no-effect by inspecting `aria-invalid`.
- React `onClick` guards that early-return based on store state.
- Form submission rejected by HTML5 validation (`<input required>` etc).
  The browser shows native error popovers but the click event itself
  fires; no observable side effect.

## What `wait_for_effect` would do

```jsonc
{
  "selector": "[data-test-id=submit-button]",
  "wait_for_effect": {
    "kinds": ["network", "dom_mutation", "navigation"],
    "timeout_ms": 1000,
    "scope": "form"   // restrict DOM mutation watch to nearest <form>
  }
}
```

Returns the existing `ClickOutput` plus an `effect_observed` field
listing what fired (and what didn't) in the window. An empty
`effect_observed` is a strong signal that the click was a no-op.

## Why this is not a small change

- Defining "effect" precisely. Network: any new request? Any non-cached?
  Same-origin only? DOM: any mutation? mutation observer is noisy on
  many SPAs; need scope filtering. Navigation: same-document
  pushState counts?
- Polling cost: each poll requires CDP roundtrips and DOM diffing.
- Existing `get_network_requests` + `query` already let the LLM check
  these post-hoc. `wait_for_effect` is convenience, not new capability.

## Re-open trigger

If exploration sessions continue to surface "click succeeded but nothing
happened" cases where the post-005 `ClickOutput` state is clean (no
warning, no error), file new findings against this issue and consider
implementing option 2.

## Notes

Surfaced during adversarial review of issue 005. The user explicitly
elected to ship without option 2, so this doc tracks the deferral
rather than treating it as in-scope work.
