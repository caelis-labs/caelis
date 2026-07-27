# Release

All Go packages share the root `vX.Y.Z` tag and release lifecycle. A release
publishes six CLI archives, checksums, six platform npm packages, and the main
`@caelis/caelis` package.

## Gate model

Quality belongs to `quality.yml`. The tag workflow waits for the successful
`main` quality run with the exact tag SHA, then builds and publishes artifacts.
It does not rerun ordinary tests, race suites, regressions, proxy smoke, or
release-dry-run.

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm `README.md` points to `https://caelis.dev`,
   `https://caelis.dev/install.sh`, and `https://caelis.dev/install.ps1`.
3. Confirm every `@caelis/*` npm trusted publisher targets
   `caelis-labs/caelis`, workflow `release.yml`, environment `default`.
4. Confirm the intended release SHA is committed and pushed to `main`. The tag
   may be pushed while its ordinary `quality.yml` run is still active; release
   automation waits for that exact run. Do not rerun `commit-check` merely
   because a tag is about to be created.
5. Prepare concise release notes for user-visible changes.

`make sdk-race`, `make regression`, `make sdk-proxy-smoke`, and
`make release-dry-run` remain available for a change that specifically needs
them. They are diagnostic tools, not unconditional release stages.

## Publish

Create an annotated tag for the quality-approved SHA and push it:

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

The workflow waits for successful exact-SHA `main` quality, runs GoReleaser,
publishes platform npm packages, and publishes the main package last.

## Post-publish acceptance

Before declaring the release complete:

1. Verify the public GitHub Release contains the six platform archives and
   `checksums.txt`.
2. Verify version `X.Y.Z` exists for all six platform npm packages and
   `@caelis/caelis`.
