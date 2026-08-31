# Release

All Go packages share the root `vX.Y.Z` tag and release lifecycle. A release
publishes six CLI archives and checksums to GitHub Releases, six platform npm
packages, and the main `@caelis/caelis` package. After those canonical outputs
succeed, the workflow mirrors the newest GitHub Release assets to the public R2
bucket used by the raw installers.

Release builds explicitly stamp `build_kind=release`, distribution version,
commit, build time, and BuildID. Unstamped/local builds are development builds
and use an isolated default Store. Updating an installed artifact is not
complete until the updated binary executes idempotent `service start` and the
selected service reports a ready identity. Windows npm foreground handoff owns
installation, version verification, service activation, and only then prints
the completion message. Deferred Windows raw replacement reports only that the
update is prepared; its replacement helper activates the service after the old
process exits.

## Gate model

Quality belongs to `quality.yml` and consists of the fast default lint, full
untagged test, and build gates. The tag workflow waits for the successful
`main` quality run with the exact tag SHA, then builds with the Go version
declared by `go.mod` and publishes artifacts. It does not rerun ordinary tests,
race suites, the broader regression target, proxy smoke, or release-dry-run.

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm `README.md` points to `https://caelis.dev`,
   `https://caelis.dev/install.sh`, and `https://caelis.dev/install.ps1`.
3. Confirm every `@caelis/*` npm trusted publisher targets
   `caelis-labs/caelis`, workflow `release.yml`, environment `default`.
4. Confirm the repository Actions secrets `R2_ACCESS_KEY_ID`,
   `R2_SECRET_ACCESS_KEY`, and `R2_ENDPOINT` are present, the bucket-scoped
   publisher credential has not expired, and `releases.caelis.dev` is active.
5. Confirm the intended release SHA is committed and pushed to `main`. The tag
   may be pushed while its ordinary `quality.yml` run is still active; release
   automation waits for that exact run. Do not rerun `commit-check` merely
   because a tag is about to be created.
6. Prepare concise release notes for user-visible changes.
7. When a release retires a durable writer but keeps a compatibility reader,
   record the last writer and this first no-write version in the release notes.
   Keep the reader until the documented minimum supported upgrade source is the
   first no-write version or newer.

`make arch-lint`, `make sdk-boundary-check`, `make client-protocol-check`,
`make docs-links`, `make sdk-race`, `make regression`, `make sdk-proxy-smoke`,
and `make release-dry-run` remain available for a change that specifically
needs them. They are change-scoped diagnostic tools, not unconditional commit,
CI, or release stages.

## Publish

Create an annotated tag for the quality-approved SHA and push it:

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

The workflow waits for successful exact-SHA `main` quality, runs GoReleaser,
publishes platform npm packages, and publishes the main package. It then checks
that the tag is GitHub's current latest release, verifies all six archives
against `checksums.txt`, uploads them under `releases/vX.Y.Z/` in R2, and updates
`latest.txt` last. Older version prefixes are removed from R2 after the new
pointer is live; GitHub Releases remains the complete versioned archive.

## Post-publish acceptance

Before declaring the release complete:

1. Verify the public GitHub Release contains the six platform archives and
   `checksums.txt`.
2. Verify version `X.Y.Z` exists for all six platform npm packages and
   `@caelis/caelis`.
3. Verify `https://releases.caelis.dev/latest.txt` contains `vX.Y.Z`, then
   download one matching archive and `checksums.txt` from
   `https://releases.caelis.dev/releases/vX.Y.Z/` and verify its checksum.
