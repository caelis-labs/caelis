# Release

All Go packages share the root `vX.Y.Z` tag. A release publishes six CLI
archives plus checksums to GitHub Releases. The workflow also publishes six
platform npm packages, the main
`@caelis/caelis` package, and mirrors the latest archives to the public R2 bucket
used by the installers and raw self-updater.

Raw installations check `https://releases.caelis.dev/latest.txt` and download
the archive and `checksums.txt` from `/releases/<tag>/` on the same host.
`CAELIS_RELEASES_BASE_URL` overrides that base URL for both installation and
self-update. The raw channel serves the latest release only and does not fall
back to GitHub; GitHub Releases retains historical versions for manual installs.
The updater verifies SHA256 before replacing the executable. Global npm
installations continue to update through npm.

Release builds stamp the distribution version, commit, build time, BuildID, and
`build_kind=release`. Local or unstamped builds remain development builds and use
their isolated default Store.

## Gate model

`.github/workflows/quality.yml` runs lint, full untagged tests, build, and
reachable-vulnerability checks for PRs targeting `main`, using GitHub's PR merge
ref to check integration. The `main` branch ruleset requires up-to-date PRs and
successful `go-quality`, `windows-host-open`, and `govulncheck` checks before
merging. Keep these rules enabled; they are the quality gate for releases.
Merging does not trigger a second quality run. Scheduled quality runs only
refresh vulnerability results.

The tag workflow verifies that the tagged commit belongs to `main`, then builds
and publishes artifacts. It relies on the protected branch rather than querying
historical CI runs or repeating ordinary tests. Maintainers must tag a reviewed
commit on `main` without bypassing its required checks. Release workflows are
serialized and do not cancel a publication already in progress.

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm README installer URLs still point to `https://caelis.dev/install.sh`
   and `https://caelis.dev/install.ps1`.
3. Confirm every `@caelis/*` trusted publisher targets this repository,
   `release.yml`, and the `default` environment.
4. Confirm `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, and `R2_ENDPOINT` are
   available and the public release domain is active.
5. Confirm the imported `github.com/caelis-labs/memory` version is released and
   declares a forward-migration floor for the persisted appliance database.
   A prerelease development baseline is a release blocker.
6. Submit the intended changes through a PR, wait for its complete quality run,
   and merge with the branch up to date. Tag the resulting commit on `main`, not
   an intermediate PR commit. Do not rerun unchanged local gates just for a tag.
7. Prepare concise user-visible release notes. When retiring a durable writer,
   record the last writer and first no-write version; retain its compatibility
   reader until the supported upgrade floor reaches that version.

Run optional architecture, SDK, protocol, race, regression, proxy, documentation,
or dry-run checks only when the release changes those boundaries. See
[Testing](testing.md).

## Publish

Create and push an annotated tag for the quality-approved SHA:

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

The workflow verifies that the tag belongs to `main`, runs GoReleaser, publishes
the platform and main npm packages, verifies the GitHub assets, mirrors them under
`releases/vX.Y.Z/` in R2, and updates `latest.txt` last. GitHub Releases remains
the complete versioned archive.

## Acceptance

Before declaring the release complete:

1. Verify GitHub Releases contains six platform archives and `checksums.txt`.
2. Verify the same version exists for all six platform npm packages and
   `@caelis/caelis`.
3. Verify `https://releases.caelis.dev/latest.txt`, download one archive and the
   checksum file from its versioned R2 prefix, and validate the checksum.
4. Verify the root module version is available through the public Go proxy.

The imported `github.com/caelis-labs/memory` module is compiled into every
Caelis binary and follows this same platform matrix, installation, and rollback
unit. Caelis does not download, stage, supervise, or version-match a separate
Memory runtime artifact. A future standalone Memory distribution remains an
independent ecosystem product and cannot become a prerequisite for this path.
