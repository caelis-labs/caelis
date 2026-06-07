# Active Rewrite Branch Cleanup

This branch is a rewrite branch, not a release branch. It keeps the active
module focused on the new architecture and uses `main` only as a behavioral
reference.

## Reference Source

Old production code remains available from `main`.

- Local reference worktree: `../caelis-main-reference`
- Git fallback: `git show main:<path>`

Do not import old production code from the active rewrite module. When old
behavior is still needed, inspect it in the reference source and port the
minimum durable semantics into the target Layer 4 or placeholder package.

## Deleted Active Roots

These roots are intentionally absent from the active rewrite branch:

- `impl/`
- `surfaces/`
- `tui/`
- `headless/`
- `eval/`
- `cmd/caelis/`
- `app/gatewayapp/`
- `internal/kernel/`
- `internal/cli/`
- `internal/acpe2eagent/`
- `internal/evalharness`
- `internal/bootstrap`
- `internal/modelcataloggen`

Architecture lint rejects Go files under these roots so accidental
reintroduction fails during `make quality`.

## Retained Placeholders

Layer 1-3 are not production-ready in this branch yet. Keep their placeholders
and TODOs so the next rewrite slices have clear landing zones:

- `app/`
- `app/commands/`
- `gateway/`
- `gateway/kernel/`

`ports/*` is temporarily retained because ACP projector/protocol code still
references upper-layer placeholder contracts. Do not use `ports/*` as the
contract pattern for newly polished Layer 4 infrastructure.

## Verification

Use the branch-local quality targets:

```bash
make quality
git diff --check
```

`make quality` validates the current active packages: Layer 4 infrastructure,
ACP protocol/terminal/projector code, retained app/gateway placeholders,
architecture lint, vet, tests, and build. `make lint` remains available as a
separate golangci-lint debt check.
