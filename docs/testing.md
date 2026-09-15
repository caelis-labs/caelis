# Testing

## Default gate

Run before committing:

```bash
make commit-check
```

On Windows it runs `make windows-check`; other platforms run configured lint,
the full untagged Go test suite, and build. `make quality` retains that full gate
on every platform. Lint includes `gofmt` and `govet`, so `make test` disables Go's
duplicate implicit vet pass. Local and sandboxed Make targets use the stable
repository-local `.tmp/cache` tree by default. CI retains its standard cache paths
for runner cache integration. Set `CACHE_ROOT=/path/to/cache` to select another persistent
location, or set it to an empty value locally to use the standard caches.

PR CI runs lint, the full suite, and build on Linux. The required
`windows-host-open` check runs `make windows-check` on native Windows. It covers
process trees, ConPTY, sandboxing, Windows paths, file locks, atomic replacement,
WAL recovery, Host persistence and replacement, client reconnection, updater
command handoff, and clipboard behavior. Small platform owners run
their full tests; large Runtime, Session, Control, Gateway, CLI, and TUI packages
use selectors in `scripts/windows_check.sh` that fail if no tests match.
Implicit vet remains enabled for Windows source. Tests use at most two packages
concurrently with a five-minute package timeout. A build of all packages and
embedded Memory Open use `CGO_ENABLED=0`, matching the release configuration.
Run additional owning tests for the changed contract; the focused gate does not
replace Linux's general coverage or change-specific Windows validation.

## Release PR CI approval

Ordinary PRs run quality checks automatically. PRs from this repository's
`release-please--branches--main` branch first wait for approval of the
`release-ci` environment. Approving that run starts the same complete Linux,
Windows, and vulnerability checks; release metadata changes do not exempt a PR
from them. See [Release](release.md#approve-release-ci) for the approval steps.

Until approval, the three required checks remain pending. Rejection or
cancellation of the approval makes the required jobs fail before checkout,
rather than reporting skipped quality checks as success. Each PR update starts
a new run requiring approval and cancels the superseded run. Scheduled
vulnerability checks continue without approval.

## Dependency update CI

Dependabot PRs run the same required checks as other PRs; review does not
replace CI, and bot jobs must not be skipped to satisfy branch protection.
To bound routine CI volume, `.github/dependabot.yml` checks weekly and permits
one open version-update PR per ecosystem (Go modules and GitHub Actions).
Minor and patch updates are grouped within each ecosystem; major updates stay
in individual PRs and share that ecosystem's limit. Security updates are not
subject to the version-update limit and are not delayed for CI approval.
The limit controls new PR creation, not CI reruns or already-open PRs.

## Change-scoped checks

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
