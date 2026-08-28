# ACP acceptance suite

`acceptance.sh` combines four deterministic gates with one real `acpx` run:

- ACP wire/projection ordering, cancellation, Spawn lifecycle, and load replay;
- product ACP typed Session lifecycle/main-Turn integration, including the
  durable product close gate, plus deterministic typed participant command
  translation and Host-owned producer-lifetime coverage;
- TUI Side ACP and Subagent overlay live/replay fidelity;
- real stdio initialize/new/prompt, process restart plus load/resume, terminal
  Task output approved by the Host's default `auto-review` path without an ACP
  client permission request, session/list, structured JSON events, and local
  `acpx` session-record close using the configured MiMo ModelProfile.

The final `acpx sessions close` checks the client-side saved-session lifecycle;
it does not send ACP `session/close`. Durable product Session close is covered by
the deterministic product typed-client gate above.

Run it from the repository root:

```bash
./eval/acp/acceptance.sh
```

Reports and raw logs persist under
`${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/acp`. The script builds an
ephemeral minimal Runtime store for the real credential and removes that store
on exit, so the persistent report directory contains no API key.
