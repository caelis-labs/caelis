# Upstream reference

This adapter ports protocol behavior from
[`agentclientprotocol/codex-acp`](https://github.com/agentclientprotocol/codex-acp).

- Audited commit: `50f69e57ca761ccafd2ca29de7fb591068277516`
- Audited package version: `1.6.2`
- Audited Codex dependency: `@openai/codex ^0.148.0`
- License: Apache-2.0, copyright 2025 JetBrains s.r.o.
- Local protocol schema used during the port: `codex-cli 0.149.1`

Ported behavior includes app-server request multiplexing, Thread lifecycle,
prompt/update translation, approvals, and full-history `session/load`. The Go
implementation intentionally does not port Node process management: the
embedding Host owns the Codex process and is the sole `Wait` caller.

The upstream `session/load` sequence (`thread/resume` followed by
`thread/read(includeTurns=true)`) is only a reference. This implementation
also validates the stored Thread CWD, installs live routing before resume, and
uses a stable whole-history read barrier before replay so `load` is not an
alias for `resume`.
