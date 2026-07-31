# Terminal-Bench 2.1 acceptance harness

This harness evaluates the locally built Caelis Headless product path against
the pinned official `terminal-bench/terminal-bench-2-1` dataset. It launches
Harbor 0.16.1 inside a detached tmux session and keeps the manifest, raw Harbor
job/trial results, structured Caelis JSONL, logs, and derived score reports
under `${XDG_STATE_HOME:-$HOME/.local/state}/caelis/evals/terminalbench`.

The persistent bundle never contains the API key. `start.sh` resolves the
selected provider's opaque credential file and the Harbor adapter uploads it
directly into each temporary task container. Harbor deletes the container after
verification. The bundle includes Harbor's public certifi CA bundle so minimal
task images can reach the configured model endpoint consistently.

Run the one-task harness smoke before the fixed five-task acceptance slice:

```bash
./eval/terminalbench/start.sh smoke
./eval/terminalbench/start.sh acceptance
```

Attach to the tmux session printed by the command, or inspect `harbor.log`,
`manifest.json`, `score.json`, and `score.md` in the printed artifact directory.
The pane remains available after completion; remove it with
`tmux kill-session -t <name>` after inspecting the run.
Set `CAELIS_EVAL_CONCURRENCY` to tune parallelism. `full` runs all 89 pinned
tasks and is intentionally opt-in because it is materially more expensive:

```bash
./eval/terminalbench/start.sh full
```

The acceptance score denominator is the fixed task set, so missing, errored, or
unscored trials count as zero. This makes interrupted runs visible instead of
silently reporting only successful verifications.
