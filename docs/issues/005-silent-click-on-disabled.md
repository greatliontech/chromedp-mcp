# `click` returns success when the click had no effect

## Summary

`click` reports success whenever the underlying `Input.dispatchMouseEvent`
succeeds, regardless of whether the click actually invoked any handler.
React onClick guards, `disabled` attributes, `pointer-events: none`, form-
validation rejections, and overlapping elements all silently swallow the
click. The LLM sees `{}` (success) and only discovers the problem by
inspecting subsequent state.

## Reproduction

1. Open an `<button disabled>...</button>` (or a React button with a
   submission guard like a form with `aria-invalid="true"` siblings).
2. `click({selector: "..."})` → `{}` returned, no error.
3. Confirm via `evaluate`: no XHR fired, no DOM change.

Real-world example from this session: Wolt's address-edit form. The "Save"
button is `disabled: false` but its onSubmit handler short-circuits when
required fields are empty. `click({selector: "[data-test-id=...submit-button]"})`
returned success; no POST fired; the form just rendered red `Required field`
errors which I had to discover by inspecting `aria-invalid`.

## Current behavior

Click is fire-and-forget. No signal about effect.

## Expected behavior

Reasonable options:

- After dispatching the click, check the target element's state and return:
  `{disabled: bool, pointer_events: "auto"|"none", visible: bool}` plus a
  warning string if any of those would have prevented the click.
- Optionally accept a `wait_for_effect` parameter that polls for either a
  network request, a DOM mutation, or a navigation within a short window
  and returns whether anything happened.
- Raise an error when the element has `disabled` attribute or
  `aria-disabled="true"` set at click time, since this is almost always a
  bug in the LLM's plan.

The `aria-invalid` case is harder to detect in the click tool itself
(validation happens in JS handlers), but a richer return value makes the
LLM's debugging cheaper.

## Workaround used

After every consequential click, run an `evaluate` that re-checks DOM state,
form validation, and recent network entries. Doubles the tool-call count
for any flow with form interactions.

## Notes

Adjacent tools (`type`, `select_option`, etc.) probably have the same
"happens but no effect" shape and would benefit from the same return-value
upgrade.
