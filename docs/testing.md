# Testing

## Default gate

Run before committing:

```bash
make commit-check
```

It runs configured lint, the full untagged Go test suite, and build. Lint already
includes `gofmt` and `govet`, so `make test` disables Go's duplicate implicit vet
pass. Local and sandboxed Make targets use the stable repository-local
`.tmp/cache` tree by default. CI retains its standard cache paths for runner
cache integration. Set `CACHE_ROOT=/path/to/cache` to select another persistent
location, or set it to an empty value locally to use the standard caches.

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

It uses locally configured DeepSeek V4 Flash with thinking disabled and GPT-5.6
Luna without an effort override. A deterministic caller drives policy, Guardian,
and native command execution against disposable files and loopback scripts.
Seatbelt or Bubblewrap must be available. The test checks approval decisions,
execution routes, file effects, and provider reasoning settings; optional
`CAELIS_GUARDIAN_COMMAND_E2E_OUT` writes per-scenario JSON evidence.
`CAELIS_GUARDIAN_COMMAND_E2E_REPETITIONS` repeats each scenario up to five times.

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
