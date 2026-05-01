# `type` appends to existing input value by default

## Summary

`type` writes the supplied text starting at the current input value, so the
result is `<existing><typed>`. There is a `clear: true` option to reset the
field first, but the default is append. For SPA forms this almost always
produces unintended state, because most fields the LLM wants to type into
are pre-populated (geocoded address, profile defaults, the previous value of
a field being edited).

## Reproduction

1. Navigate to a page with a pre-filled input (e.g. Wolt's
   `/me/addresses` → "Add new address" → the address-query input is
   pre-filled with the user's default address string).
2. `type({selector: "[data-test-id=address-query-input]", text: "makariou 1 nicosia"})`.
3. Inspect `inp.value`:

   ```
   "Ippokratous 14, 2006, Strovolos, Cyprusmakariou 1 nicosia"
   ```

   The autocomplete then doesn't fire usefully because the concatenation
   doesn't geocode.

## Current behavior

Append. `clear: false` is the default.

## Expected behavior

For LLM ergonomics, `clear: true` should be the default. The append case is
useful but rare (e.g. typing into a contenteditable that's mid-composition).

If flipping the default is too breaking, document the gotcha prominently in
the tool description so the LLM knows to set `clear: true` upfront.

## Workaround used

Always pass `clear: true`. It works once you know to do it; the issue is
that the first attempt silently corrupts the field with no error and you
spend a turn diagnosing why the next step fails.

## Notes

Could be combined with a small QoL addition: a result field saying
`previous_value` and `new_value` so the LLM can sanity-check what it just
typed.
