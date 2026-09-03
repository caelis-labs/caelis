# Release

All Go packages share the root `vX.Y.Z` tag. A release publishes six CLI
archives plus checksums to GitHub Releases. The workflow also publishes six
platform npm packages, the main
`@caelis/caelis` package, and mirrors the latest archives to the public R2 bucket
used by the installers.

Release builds stamp the distribution version, commit, build time, BuildID, and
`build_kind=release`. Local or unstamped builds remain development builds and use
their isolated default Store.

## Gate model

`.github/workflows/quality.yml` owns the exact-SHA lint, full untagged test, and
build gates. The tag workflow waits for a successful `main` quality run at the
tagged SHA before publishing; it does not repeat ordinary tests or optional
change-scoped gates.

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm README installer URLs still point to `https://caelis.dev/install.sh`
   and `https://caelis.dev/install.ps1`.
3. Confirm every `@caelis/*` trusted publisher targets this repository,
   `release.yml`, and the `default` environment.
4. Confirm `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, and `R2_ENDPOINT` are
   available and the public release domain is active.
5. Confirm the imported `github.com/caelis-labs/memory` version is released,
   declares a forward-migration floor for the persisted appliance database,
   and exposes side-effect-free authority validation that proves the configured
   View matches its Grant. A prerelease development baseline is a release
   blocker.
6. Commit and push the intended SHA to `main`, then wait for or identify its
   quality run. Do not rerun unchanged local gates merely because a tag is next.
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

The workflow waits for exact-SHA quality, runs GoReleaser, publishes the platform
and main npm packages, verifies the GitHub assets, mirrors them under
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
