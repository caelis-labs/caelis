# Testing

## Default gate

Run before committing:

```bash
make commit-check
```

`make commit-check` checks Go formatting, runs the full `make lint` target, and
checks staged/unstaged diff whitespace on all platforms. It requires
`golangci-lint` on `PATH`; use the version pinned in
[the quality workflow](../.github/workflows/quality.yml). Run focused owning tests
while changing code, plus the relevant checks below. PR CI runs lint, full tests,
and build; `make quality` remains available when a full local run is useful. Do
not repeat unchanged passing checks just to commit or push. Lint includes
`govet`, so `make test` disables Go's implicit vet pass and belongs with lint in
the full gate.

Local and sandboxed Make targets use the stable repository-local `.tmp/cache`
tree by default. CI uses standard cache paths for runner cache integration.
Set `CACHE_ROOT=/path/to/cache` to select another persistent location, or set it
to an empty value locally to use standard caches.

## PR checks

The `quality` workflow runs only on PRs targeting `main`, checks GitHub's PR
merge ref, and cancels superseded runs for the same PR. Its `changes` job
classifies the complete Git diff against the PR base, regardless of the author
or branch name:

| Changed files | Checks |
| --- | --- |
| Root README files, `AGENTS.md`, `agent-sdk/README.md`, `docs/**/*.md` | Maintained Markdown links |
| `.release-please-manifest.json`, `CHANGELOG.md` | Valid root version, increasing version when changed, matching first changelog heading, regular file types |
| Any other path, or executable/symlink prose | Linux lint, full untagged tests, build, reachable-vulnerability scan |
| Mixed changes | All applicable checks above |

Embedded prompts, test fixtures, Go dependencies, scripts, and workflow changes
receive full checks. Release-only PRs need neither a Go toolchain nor manual CI
approval. The single required `quality` result fails if classification, metadata
validation, documentation validation, or any selected job fails or is cancelled.
Only jobs outside the selected scope may be skipped.

The branch rule does not require chasing the latest `main`. Advancing `main`
alone does not force another PR update and rerun. Checks prove the merge tree
at the time of the run; later combinations with newly merged changes are not
automatically retested. Resolve actual conflicts and rerun checks on any changed
candidate. Merging a PR does not trigger another full quality run.

## Daily and manual platform checks

`platform-checks.yml` runs daily on `main` and can be started from Actions with
**Run workflow** for a selected branch. It runs reachable-vulnerability scans,
Guardian/model invocation race tests, and `make windows-check` on native Windows.
These jobs are outside the ordinary PR merge gate; platform or race regressions
can therefore be discovered after merging. Run the workflow before merging when
a change specifically needs native platform evidence.

The Windows gate covers process trees, ConPTY, sandboxing, paths, file locks,
atomic replacement, WAL recovery, Host persistence and replacement, client
reconnection, updater handoff, and clipboard behavior. Small platform owners run
their full tests; larger packages use selectors in `scripts/windows_check.sh`
that fail if no tests match. Implicit vet remains enabled. Tests use at most two
packages concurrently with a five-minute package timeout. A build of all packages
and embedded Memory Open use `CGO_ENABLED=0`, matching the release configuration.

## Dependency update CI

Dependabot PRs run the same required checks as other PRs; review does not
replace CI, and bot jobs must not be skipped to satisfy branch protection.
To bound routine CI volume, `.github/dependabot.yml` checks weekly and permits
one open version-update PR per ecosystem (Go modules and GitHub Actions).
Minor and patch updates are grouped within each ecosystem; major updates stay
in individual PRs and share that ecosystem's limit. Security updates are not
subject to the version-update limit.
The limit controls new PR creation, not CI reruns or already-open PRs.

## Change-scoped checks

The following Jev evaluations are opt-in and send only repository synthetic fixtures. Set
`JEV_API_KEY` in the test process environment and run the scenarios in order:

```bash
CAELIS_JEV_EVAL=1 go test ./agent-sdk/tool/builtin/toolsearch -run '^TestToolSearchJevEvaluation$' -count=1 -v
CAELIS_JEV_EVAL=1 go test ./app/gatewayapp -run '^TestMemoryJevEvaluation$' -count=1 -v
```

These tests report outcomes, token counts and elapsed time. A passing test means
the evaluation completed; assess the reported semantic scores separately. Small
synthetic samples do not establish production accuracy or adversarial robustness.
Ordinary tests never load `.env` or make live Jev requests.

`TestGuardianJevScreeningEvaluation` uses `CAELIS_JEV_EVAL=1` and `JEV_API_KEY`
for a synthetic corpus of direct reads and shell operations, credential export,
remote mutations, user constraints, opaque code and hooks, dynamic targets, instruction injection,
user corrections and approval-option scope. Each case carries an independent
expected route and rationale. `CAELIS_JEV_SCREENING_SET=pilot|holdout|smoke|all`
selects a set (default `all`); pilot and holdout are disjoint, while smoke is a
pilot subset. `CAELIS_JEV_SCREENING_SAMPLES` selects 3–5 repeats (default 4).

The evaluation exercises the production reviewer, recording the complete
synthetic request, Choice distribution, both Noul answers, model version, usage,
latency and actual direct/deferred route. `CAELIS_JEV_SCREENING_OUT` optionally
writes these per-sample rows to JSON. Clear allow/deny cases may defer, but
missing-evidence cases must not settle directly. Wrong direct decisions, expanded
option scope and provider errors fail the check; provider failures do not count as
successful abstentions. Safe/risky Read-history pairs must have identical
classifier requests despite different Agent evidence. No proposed command is
executed, referenced credential file read, or private Session sent. Agent fallback
stops at model resolution without a generative request. The corresponding
`TestGuardianScreenEvaluationFixtures` and `TestGuardianScreenEvaluationHistoryPairs`
checks run offline.

`TestGuardianJevCascadeE2E` uses `CAELIS_JEV_CASCADE_E2E=1`, `JEV_API_KEY`,
and `CAELIS_GUARDIAN_E2E_MODEL` (a configured local generative model alias) to
exercise the approval reviewer with real Jev and Agent providers. It records the
rendered synthetic classifier request, distribution, actual route and final
settlement. Legal non-screenable option aliases must reach the Agent without a
Jev call; canonical options may settle directly or defer. Wrong decisions and
provider failures fail the check, without a direct-coverage target. Proposed
commands are never executed and no private Session is replayed.

`TestGuardianSessionJevReplay` is a separate, explicit opt-in for an existing
Session's evidence. Set `CAELIS_JEV_SESSION_E2E=1`, `JEV_API_KEY`, and
`CAELIS_JEV_SESSION_EVENTS` to its `.events.jsonl` file; the adjacent `.json`
metadata must exist. It reads the source, rebuilds approval checkpoints in a
throwaway in-memory Session, and calls the real Jev classifier for at most eight
approvals. It never executes historical tools or mutates the source Store.
Use a Session whose approvals are known to be allowed. Confident denials and
provider failures fail the check; low-confidence deferrals are reported separately
and do not count as saved Agent requests. Set `CAELIS_JEV_SESSION_E2E_OUT` for a
JSON report containing only decisions, scores, request sizes, usage and timing.
This replay does not call the fallback Agent; deterministic cascade tests verify
fallback and receipt persistence. It sends private Session evidence to the selected
provider, so it must only be run for an explicitly selected, authorized Session.

The following checks remain explicit because repeating them for every change
adds cost without improving unrelated changes:

| Change | Check |
| --- | --- |
| Imports, package ownership, gateway/eventstream boundaries | `make arch-lint` |
| Public AppServer schema, Envelope JSON, generated clients | `make client-protocol-check` |
| SDK dependencies or supported imports | `make sdk-boundary-check` |
| Runtime, Control, projection, or physical TUI contracts | `make product-acceptance` |
| Broad TUI, command, or ACP integration | `make regression` |
| In-process Host or built-in Memory composition | `make startup-performance` |
| Native Windows Host Memory Open | `go test ./app/gatewayapp/internal/memoryhost -run TestEmbeddedHostBindsSDKClient` |
| Managed Host lifecycle or process ownership | `go test -race ./internal/servicelifecycle ./internal/cli` |
| Maintained documentation links | `make docs-links` |
| npm launcher or package handoff | `npm --prefix npm test` |
| Release assembly | `make release-dry-run` |

Concurrency, lease, persistence, broker, and lifecycle changes also require the
narrowest relevant `go test -race` package. File locking, atomic replacement,
and WAL recovery require native Windows evidence when Windows behavior changes;
cross-compilation is not equivalent.

Codex ACP has two opt-in live tests:

```bash
CAELIS_CODEX_TIER_E2E=1 go test ./adapters/codex -run '^TestLiveCodexServiceTiers$' -count=1 -timeout=3m -v
CAELIS_CODEX_COLLABORATION_E2E=1 go test ./app/gatewayapp -run '^TestLiveCodexControllerCollaboration$' -count=1 -timeout=5m -v
```

Both require an installed Codex CLI and authenticated account. They copy only
authentication into a temporary `CODEX_HOME`, leaving the user's configuration
and conversations unchanged. They make real model requests; the tier test
includes Fast requests. `CAELIS_CODEX_TIER_MODEL` selects the model (default
`gpt-5.6-luna`). Optional `CAELIS_CODEX_TIER_E2E_OUT` records service-tier request
and response fields; `CAELIS_CODEX_COLLABORATION_E2E_OUT` records canonical
participants, Tasks, creation journals and explicit peer messages, without
credentials. The collaboration test uses a real Codex controller and ACP child
with a deterministic native provider, checking actual Caelis ownership rather
than the model's final answer. Its approval resolver allows only the test's
Caelis collaboration calls.

Guardian command approval has an opt-in live test:

```bash
CAELIS_GUARDIAN_COMMAND_E2E=1 go test ./app/gatewayapp -run '^TestGuardianCommandE2E$' -count=1 -timeout=30m -v
```

It defaults to locally configured `deepseek-v4-flash` with thinking disabled and
`gpt-5.6-luna` without an effort override. Set
`CAELIS_GUARDIAN_COMMAND_E2E_MODELS` to comma-separated local model names to select
other configured aliases. A deterministic caller drives policy, Guardian,
and native command execution against disposable files and loopback scripts.
Seatbelt, Bubblewrap or the native Windows sandbox must be available. Windows
fixtures use PowerShell. The test checks approval decisions,
execution routes, file effects, and provider reasoning settings; optional
`CAELIS_GUARDIAN_COMMAND_E2E_OUT` writes per-scenario JSON evidence.
The caller uses zero inline yield, observes the same Task until it settles,
and records time to the first result separately from approval and execution.
`CAELIS_GUARDIAN_COMMAND_E2E_REPETITIONS` repeats each scenario up to five times.

`CAELIS_GUARDIAN_RESIDENT_E2E=1` enables `TestGuardianResidentE2E` with the local
`gpt-5.6-luna` alias. It exercises 48 successive development approvals across
built-in and external main/subagent origins, concurrent review queueing, task
conflicts, injected tool output and evidence failures. Failure scenarios select a
reproducible tool operation before the real model makes its judgment. The output
directory also receives resident review metrics. Ordinary approvals require at
least 90% without tools and Runtime reuse while the window still fits. P50/P95
latency and cache usage are reported for comparison under the same provider and
settings, not enforced as universal provider speed limits. Slow native evidence
and recoverable tool failures must still permit a final judgment for both allowed
and rejected actions; no incorrect allow or deny is accepted. Command E2E verifies
real execution effects separately. Deterministic context tests cover successive
approvals, user steering, late results and diagnostic output from successful
wrappers without history lookup or sandbox setup. Availability tests cover
25/60/89-second valid responses, total deadline expiry, shared queue budgets,
late producer cleanup, long user-message constraints and unavailable active
context overflow without repeated tool execution.
Provider latency/cache counters are observations,
not deterministic unit-test assertions or guarantees from the Harness.

`CAELIS_BOT_E2E=1` enables `TestBotRealMimoConversation`, a bounded real-provider
Bot notebook test. It copies the configured
`provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5` profile and its credential into a
disposable Store, leaving the user's Host, configuration, and conversations
untouched. Four synthetic prompts cover a natural preference, explicit saving,
revision, and index-led reading after Host restart. The test checks actual note
and index contents, canonical Read calls after restart, the exact five-tool
notebook set, and unchanged provider message/tool prefixes. It reports natural
note-taking selection separately from these reliability assertions. The budget
is four minutes and at most 28 provider requests; no mock result substitutes for
real model behavior.

```bash
CAELIS_BOT_E2E=1 CAELIS_BOT_SOURCE_STORE=$HOME/.caelis CAELIS_BOT_E2E_OUT=/tmp/caelis-bot-evidence.json go test ./app/gatewayapp -run '^TestBotRealMimoConversation$' -count=1 -timeout=5m -v
```

`CAELIS_BOT_SOURCE_STORE` selects the Store holding the configured provider and
credential. `CAELIS_BOT_E2E_OUT` is optional; when set, it writes only the test's
synthetic request payloads, replies, note contents, and the natural note-taking
observation, never headers or credentials.

Deterministic Bot coverage in the same package needs no provider. It provisions a
Bot created before notebooks were universal on that owner's first prompt, proves
the notebook is created exactly once and that a deleted `index.md` is never
recreated, and rejects the prompt with an explicit error when the Store cannot be
provisioned at all.

`make windows-check` runs Guardian's native evidence tests with deterministic
model responses. They exercise PowerShell, temporary-only writes, file evidence,
the approval tool loop and Windows' always-enabled network behavior.

## Product scenarios

`make product-acceptance` selects deterministic cross-layer tests through
`scripts/go_test_nonempty.sh`, so a renamed or removed selector cannot silently
pass with zero tests. These tests are already part of `make test`; the focused
target is for change validation, not a second universal CI pass.

A product scenario may compare:

1. canonical Runtime `session.Event` facts;
2. projected `eventstream.Envelope` values;
3. normalized full terminal frames from the physical VT harness.

Drive the production entry point with deterministic dependencies, compare whole
objects or event sequences, and keep layer-specific setup beside the owner under
test. Persistence or replay changes must additionally prove that rebuilt model
context matches Runtime-produced context.
