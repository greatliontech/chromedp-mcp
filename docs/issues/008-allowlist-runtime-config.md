# Profile allowlist requires a server restart to change

## Summary

`--allowed-profiles` is read once at server start. To add or remove a
profile, the operator must edit `~/.claude.json` (or wherever the MCP is
configured) and restart the host. There's no MCP tool to list configurable
profiles, request a profile addition, or reload the config.

This makes the "discover a profile, ask to use it" loop awkward: the LLM
has to first explain to the user where to edit the config, the user
restarts the host, the LLM resumes.

## Reproduction

1. Configure `--allowed-profiles Foo`.
2. Start the MCP. `browser_list_profiles` returns `Foo`.
3. The user has another profile `Bar` that exists on disk in
   `--profile-dir` but isn't allowlisted.
4. `browser_launch({profile: "Bar"})` → rejected.
5. The only path forward is: edit the config, restart, retry.

This session triggered exactly this — `glt` existed under the chromedp-mcp
profile dir as `Profile 1` but wasn't in `--allowed-profiles`. Resolution
took an edit to `~/.claude.json` and a Claude Code restart.

## Current behavior

Allowlist is static for the server's lifetime. No introspection of "what
profiles exist on disk that are *not* allowlisted" — `browser_list_profiles`
correctly hides un-allowlisted profiles, which is good security but means
the LLM can't even tell the user "you have a profile called X you might
want to allowlist."

## Expected behavior

Reasonable options, in increasing intrusiveness:

- A separate tool `list_known_profiles_unsafe()` that returns *all* profiles
  on disk (name + dir), explicitly documented as "not gated by allowlist;
  use to suggest allowlist additions to the operator." Returns names only,
  no cookies/data.
- Re-read `--allowed-profiles` on SIGHUP (or watch the config file) so the
  operator can edit the file and not restart the entire host.
- Interactive permission prompt: when `browser_launch({profile: "X"})` is
  called for an un-allowlisted profile that exists on disk, the MCP could
  prompt the operator (out-of-band stdin, native dialog, whatever the host
  supports) to one-shot allow it. Higher friction to implement.

## Workaround used

Added `glt` to `--allowed-profiles candosa,glt` in `~/.claude.json` and
restarted Claude Code. Worked, but the restart kills the conversation
context.

## Notes

The security posture of the current allowlist is sound — silently exposing
profiles would be worse. The gap is purely UX: there's no friendly path to
escalate "I want to use this profile" without an out-of-band restart.
