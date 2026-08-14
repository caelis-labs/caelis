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

This harness is a solver-capability lane. Caelis starts with
`--dangerously-skip-permissions`, so tools execute directly in the temporary
task container without sandbox isolation, human approval, or Guardian review.
The built-in destructive-command blacklist remains active, but it is not a
security boundary. Run sandbox and Guardian integration checks separately.

Run the one-task harness smoke before the fixed five-task acceptance slice:

```bash
./eval/terminalbench/start.sh smoke
./eval/terminalbench/start.sh prior-failures
./eval/terminalbench/start.sh acceptance
```

Attach to the tmux session printed by the command, or inspect `harbor.log`,
`manifest.json`, `score.json`, `score.md`, `audit.json`, and `audit.md` in the
printed artifact directory. Every run has one immutable run ID and keeps its
prepared bundle, start/worker logs, complete Harbor job tree, per-trial Caelis
JSONL/stderr, verifier output, reports, and content-addressed evidence index in
that directory. The audit index records the SHA-256 and size of every collected
file and maps each task to its trace, timing, token usage, reward, and verifier
evidence.
The pane remains available after completion; remove it with
`tmux kill-session -t <name>` after inspecting the run.
The harness refuses to start from a dirty Git worktree. Its manifest records the
full commit, branch, binary/config/harness/task-list digests, runtime versions,
model profile and concrete model config, reasoning effort, concurrency, timeout
multiplier, timestamps, and execution mode. Automatic Harbor retries are
disabled so a failed attempt can never be deleted and replaced beneath the same
run ID. Infrastructure retries must be launched as a separate run.

Set `CAELIS_EVAL_CONCURRENCY` to tune parallelism,
`CAELIS_EVAL_REASONING_EFFORT` to a level supported by the selected model
profile, and `CAELIS_EVAL_TIMEOUT_MULTIPLIER` to record and apply an explicit
timeout multiplier. For example, the DeepSeek v4 flash max preview is:

```bash
CAELIS_EVAL_MODEL_PROFILE=provider:deepseek@default/deepseek/deepseek-v4-flash \
CAELIS_EVAL_PROVIDER_ENDPOINT=deepseek@default \
CAELIS_EVAL_REASONING_EFFORT=max \
./eval/terminalbench/start.sh acceptance
```

`full` runs all 89 pinned tasks and is intentionally opt-in because it is
materially more expensive:

```bash
./eval/terminalbench/start.sh full
```

Token totals are summed from unique canonical `usage_update` events, one per
model invocation. A trace with a normal terminal record is marked `complete`;
a timeout prefix is retained and marked `partial_lower_bound` instead of being
presented as complete billing usage. Every JSONL Envelope is also decoded by the
repository's typed `wirev1` reader before the report can be complete.

By default, `start.sh` also prepares two verifier-only cache services in Docker:
an Apt-Cacher NG proxy for Debian indexes/packages and a local mirror of the
checksum-pinned uv 0.9.5 x86_64/aarch64 glibc and musl Linux release assets
used by Terminal-Bench 2.1. Only the
verifier process receives `http_proxy` and `UV_DOWNLOAD_URL`; the official task
image, task definition, tests, and Agent environment remain unchanged. The
first setup pulls/builds the cache images and fills the named APT volume. Later
trials and harness runs reuse both, including after starting on a newly
provisioned machine and completing its initial downloads. Cache image, volume,
content digest, and reuse metadata are recorded in the run manifest and score
report. Set `CAELIS_EVAL_VERIFIER_CACHE=disabled` to run the unaccelerated
official network path. `CAELIS_EVAL_APT_CACHE` remains accepted as a legacy
alias when the new variable is unset.

The cache uses these narrowly named Docker resources:

- image `caelis/terminalbench-apt-cache:<config-digest>`
- image `caelis/terminalbench-uv-cache:<config-digest>`
- container `caelis-terminalbench-apt-cache`
- container `caelis-terminalbench-uv-cache`
- network `caelis-terminalbench-cache`
- volume `caelis-terminalbench-apt-cache-v1`

The volume intentionally survives trials and harness runs. Removing it makes
the next verifier run a cold-cache run.

The acceptance score denominator is the fixed task set, so missing, errored, or
unscored trials count as zero. This makes interrupted runs visible instead of
silently reporting only successful verifications.
