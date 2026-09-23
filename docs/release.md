# Release

All Go packages share the root `vX.Y.Z` tag. A release publishes six CLI
archives plus checksums to GitHub Releases. The workflow also publishes six
platform npm packages, the main
`@caelis/caelis` package, and mirrors the latest archives to the public R2 bucket
used by the raw installers.

Raw installations check `https://releases.caelis.dev/latest.txt`. The official
`https://caelis.dev/install.sh` and `https://caelis.dev/install.ps1` scripts own
archive download, SHA256 verification, extraction, and executable replacement.
Raw self-update runs the same scripts against the current installation directory,
using Bash on macOS/Linux and PowerShell on Windows, then verifies the installed
binary's release identity. `CAELIS_RELEASES_BASE_URL` overrides the archive base
URL for both installation and self-update. The raw channel serves the latest
release only and does not fall back to GitHub; GitHub Releases retains historical
versions for manual installs. Global npm installations update through npm.

`caelis update` and TUI `Ctrl+U` install artifacts without stopping or starting a
Host. The next managed application launch owns Host version selection and
activation, including interruption of active Sessions; see
[Product Host and clients](architecture.md#product-host-and-clients). Installation
success does not mean an already-running client or Host has changed version.

Release builds stamp the distribution version, commit, build time, BuildID, and
`build_kind=release`. Local or unstamped builds remain development builds and use
their isolated default Store.

## Application Runtime upgrades

The application protocol replaces the removed Bot Mode; there is no legacy Bot
CLI, scheduler, storage API or compatibility entry point. Applications own
identity, Notebook, business tools and scheduling. Caelis provides generic
execution, permissions, observation and recovery. See
[Application Runtime](application-runtime.md) for the public contract.

The Application Store migration is separate from embedded Memory below. Its
schema 1 → 2 upgrade preserves bindings, Session identities, operation
requests/digests/receipts, callback receipts and resource bytes. Existing native
Sessions retain their old RunCommand/Task catalog plus the resource bridge;
new file tools are selected explicitly rather than silently granted. The
[compatibility contract](application-runtime.md#persistence-compatibility) owns
the supported upgrade source and immutable creation retry behavior.

A schema-1 binary cannot open the upgraded Application Store. Before upgrading
when rollback is required, stop the Host, prevent client restarts and keep a
complete protected Store backup, including database WAL/SHM files, configuration
and credentials. Verify the backup in isolation. Downgrading the executable
alone cannot undo either database migration; reconcile post-upgrade writes
before restoring a backup.

CWD, inheritance, execution and permissions remain bound at Session creation.
Model, reasoning effort, supported service tier, instructions and tools can
change at the next unissued model request, including within a Turn. A provider's
support for priority is not a promise that every model supports Fast. Synthetic
provider checks cannot establish real-model or Fast service behavior. Release
notes must distinguish actual model requests from deterministic recovery tests
and limit live acceptance to the exercised models, adapter, and request paths;
a successful priority request does not establish latency, billing, or GUI and
distribution acceptance.

## Embedded Memory upgrades and recovery

The embedded Memory database uses schema 2. Opening a supported v0.5.2
schema-1 database migrates it during Host construction. The migration is
transactional, but there is no automatic pre-upgrade backup or supported
in-place downgrade. Binaries embedding Memory v0.5.2 cannot open schema 2.

Before the first Host launch with a newer Memory schema:

1. Stop the Host with `caelis service stop` and prevent clients from restarting
   it while the backup is taken.
2. Preserve a complete, access-protected copy of the Store, including Memory
   data, any SQLite WAL/SHM files, configuration, and credentials. Verify that
   the backup can be restored in isolation before relying on it for rollback.
3. Start the new Host and verify Memory access and, when configured, Steward
   processing. Retain the pre-upgrade backup until the upgrade is accepted.

Restoring an older binary alone does not restore its database format. Recovery
requires a compatible pre-upgrade Store backup; reconcile later writes and
forgetting/deletion operations before resuming use so deleted evidence is not
revived. Never delete Memory data or overwrite immutable Steward profiles to
resolve an upgrade error. See the Memory owner's
[migration contract](https://github.com/caelis-labs/memory/blob/v0.6.1/docs/memory-v0.6-migration.md).

## Gate model

The `main` branch ruleset requires the aggregate `quality` check from GitHub
Actions. It does not require PR branches to be up to date. The workflow selects
checks from actual changed files: code runs the Linux quality gate; maintained
prose and release metadata use their specific validators. A failed classifier
or selected check blocks merging. See [Testing](testing.md#pr-checks) for the
scope rules and the integration tradeoff of allowing main to advance.

Native Windows and targeted race checks run daily or manually, outside the
ordinary PR gate. Before publishing platform-sensitive changes, obtain the
relevant native evidence through `platform-checks.yml`. Merging does not trigger
a second full quality run.

The tag workflow verifies that the tagged commit belongs to `main`, then builds
and publishes artifacts. It relies on the protected branch rather than querying
historical CI runs or repeating ordinary tests. Maintainers must tag a reviewed
commit on `main` without bypassing its required checks. Release workflows are
serialized and do not cancel a publication already in progress.

## Release bot setup

The `release-please` workflow maintains one Release PR targeting `main`. It
updates the root version in `.release-please-manifest.json` and generates
`CHANGELOG.md`. All Go and npm packages continue to share that version; npm
manifests are stamped from the tag during publication. The bot updates its PR
when release contents change, without forcing updates solely to catch up with
`main`.

Create a dedicated fine-grained PAT scoped to `caelis-labs/caelis`, with
**Contents**, **Issues**, and **Pull requests** set to **Read and write**.
The token owner must have repository write access; complete any organization
approval required for the token. Store it as `RELEASE_PLEASE_TOKEN` in repository
Actions secrets or in an organization Actions secret shared with this repository,
and renew it before expiration. Do not grant the bot a branch-protection bypass
or enable automatic merging.

The dedicated token lets bot-created PRs and tags trigger the existing quality
and release workflows. The default `GITHUB_TOKEN` suppresses those downstream
runs; see [release-please authentication](https://github.com/googleapis/release-please-action#other-actions-on-release-please-prs).
A missing secret fails with a setup diagnostic. After adding or rotating it,
run the `release-please` workflow manually on `main` if a retry is needed.

Use Conventional Commit PR titles and squash merge so the resulting commits
retain their release meaning: `fix:` produces a patch, `feat:` produces a
minor release, and `feat!:` or `BREAKING CHANGE:` records an incompatible change.
While the version is below `1.0.0`, incompatible changes also bump the minor
version. `chore:` and `docs:` alone do not open a Release PR. For an intentional
version override, use a `Release-As: X.Y.Z` footer in a merged commit, following
[release-please version overrides](https://github.com/googleapis/release-please#how-do-i-change-the-version-number).

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm README installer URLs still point to `https://caelis.dev/install.sh`
   and `https://caelis.dev/install.ps1`.
3. Confirm every `@caelis/*` trusted publisher targets this repository,
   `release.yml`, and the `default` environment.
4. Confirm `RELEASE_PLEASE_TOKEN`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, and
   `R2_ENDPOINT` are available and the public release domain is active.
5. Confirm the imported `github.com/caelis-labs/memory` version is released and
   declares a forward-migration floor for the persisted appliance database.
   A prerelease development baseline is a release blocker. For a schema change,
   include the backup and recovery requirements above in the release notes.
6. Submit the intended changes through PRs and wait for their selected checks.
   Review the resulting Release PR's version and changelog, and wait for its
   metadata validation. Merging this PR is the decision to publish; there is no
   separate CI approval step. Do not rerun unchanged local gates just for a tag.
7. Ensure the release notes are concise and user-visible. When retiring a durable writer,
   record the last writer and first no-write version; retain its compatibility
   reader until the supported upgrade floor reaches that version.

Run optional architecture, SDK, protocol, race, regression, proxy, documentation,
or dry-run checks only when the release changes those boundaries. See
[Testing](testing.md).

## Publish

Merge the reviewed Release PR after `quality` passes when ready to publish. The
bot creates the root `vX.Y.Z` tag at that merge commit and a GitHub Release containing the
changelog. That tag triggers `release.yml`: GoReleaser attaches the CLI archives
and checksums while preserving the bot's release notes. The GitHub Release may
be visible before all distribution steps have finished; complete the acceptance
checks below before announcing availability.

The workflow then publishes the platform and main npm packages, verifies the
GitHub assets, mirrors them under `releases/vX.Y.Z/` in R2, and updates `latest.txt`
last. GitHub Releases remains the complete versioned archive.

### Manual fallback

If the bot is unavailable, first merge a reviewed PR updating the root manifest
version and changelog to the intended release. Close any superseded Release PR.
Create and push an annotated tag for that quality-approved SHA on `main`:

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

The same artifact workflow handles manual tags. Keep the manifest aligned with
the released tag so the bot resumes from that version; never move a published tag.

## Acceptance

Before declaring the release complete:

1. Verify GitHub Releases contains six platform archives and `checksums.txt`.
2. Verify the same version exists for all six platform npm packages and
   `@caelis/caelis`.
3. Verify `https://releases.caelis.dev/latest.txt`, download one archive and the
   checksum file from its versioned R2 prefix, and validate the checksum.
4. Verify the root module version is available through the public Go proxy.

The imported `github.com/caelis-labs/memory` module is compiled into every
Caelis binary and follows this same platform matrix and installation unit.
Executable replacement does not roll back Memory's durable schema. Caelis does
not download, stage, supervise, or version-match a separate
Memory runtime artifact. A future standalone Memory distribution remains an
independent ecosystem product and cannot become a prerequisite for this path.
