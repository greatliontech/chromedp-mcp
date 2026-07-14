# Issues

Parked deferrals. Each entry carries a `Lands:` trigger saying when it comes back.

| Issue | Summary | Lands |
| ----- | ------- | ----- |
| [oopif-dom-mutations](oopif-dom-mutations.md) | DOM mutation counts cannot see inside out-of-process (cross-origin) iframes; they are reported as a lower bound via `dom_mutations_partial` rather than counted | When cross-origin iframe DOM inspection is required by a tool, or when the server attaches to OOPIF targets for any other reason |
