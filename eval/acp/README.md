# ACP acceptance suite

`acceptance.sh` combines three deterministic gates with one real `acpx` run:

- ACP wire/projection ordering, cancellation, Spawn lifecycle, and load replay;
- product ACP typed Session lifecycle/main-Turn integration;
- TUI Side ACP and Subagent overlay live/replay fidelity;
- real stdio initialize/new/prompt, process restart plus load/resume, terminal
  Task output with ACP client approval, session/list, structured JSON events,
  and durable close using the configured MiMo ModelProfile.

Run it from the repository root:

```bash
./eval/acp/acceptance.sh
```

Reports and raw logs persist under
`${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/acp`. The script builds an
ephemeral minimal Runtime store for the real credential and removes that store
on exit, so the persistent report directory contains no API key.
