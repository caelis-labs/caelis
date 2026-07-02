# ACP Projection Architecture

Caelis presentation surfaces share one protocol: standard ACP-shaped payloads
plus documented optional `_meta` extensions. TUI, ACP stdio/server, headless,
and future GUI surfaces consume this protocol and should not own runtime,
control, tool, sandbox, stream, or persistence semantics.

```text
Agent Runtime / SDK -> Control layer -> eventstream.Envelope -> surfaces
```

The control layer may bridge local runtime events, system-managed agent events,
or external ACP-agent updates. Surfaces should not need to know which source
produced an event once it has been normalized into `eventstream.Envelope`.

## Terminal Projection

RUN_COMMAND, Bash-compatible command tools, and SPAWN share the same terminal
projection contract:

- `_meta.terminal_info`: local terminal identity for a tool call.
- `_meta.terminal_output`: exact output bytes in `data`.
- `_meta.terminal_exit`: local terminal termination state when known.
- `content[type="terminal"]`: an empty render anchor with the same terminal id.

The empty terminal anchor mounts a panel; it is not an output transport and must
not contain terminal text. ACP stdio, TUI, headless, and future GUI surfaces
must render bytes from `_meta.terminal_output` and avoid surface-private
terminal fallbacks.

Standard ACP client-created terminals remain reserved for execution that is
actually delegated to a client-created ACP terminal id.

## Session Identity

`session.SessionID` is globally unique within one filestore root. Workspace key
is creation/listing/display metadata and may participate in policy decisions,
but it is not part of session identity.

ACP and gateway surfaces must pass the session id they received and must not
keep in-memory `sessionId -> workspace/cwd` caches to repair later requests.

Before v1.0, unsupported old session/index layouts may fail explicitly. Caelis
prefers the clean identity model over compatibility fallbacks.
