# Release

All Go packages share the root `vX.Y.Z` tag. A release publishes six CLI
archives plus checksums to GitHub Releases. The workflow also publishes six
platform npm packages, the main
`@caelis/caelis` package, and mirrors the latest archives to the public R2 bucket
used by the raw installers.

The tag workflow builds all six archives without publishing them. A macOS
runner imports the organization Developer ID Application certificate into an
ephemeral keychain, signs both Darwin CLI binaries with hardened runtime and
timestamp, and submits a ZIP containing those exact binaries to Apple. Only an
Accepted notarization result and matching Apple log allow final Darwin archives
and six-archive checksums to be assembled. GitHub assets, npm packages, and R2
all use these final archives. Signing or notarization failure blocks formal
Darwin publication; Linux and Windows retain the GoReleaser build path.

Apple cannot staple a ticket to a ZIP or standalone Mach-O CLI. The ZIP is a
notarization submission container, not a separately distributed product. Apple
publishes the accepted ticket online for Gatekeeper to find by code hash. The
distributed format remains the `tar.gz` CLI archive, with the same signed
binary in the Darwin npm package. Offline stapled-ticket verification is not
available for this CLI format. See [Apple's notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow).

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
activation through idle-only replacement; explicit restart may interrupt active
Sessions. See
[Product Host and clients](architecture.md#product-host-and-clients). Installation
success does not mean an already-running client or Host has changed version.

Release builds stamp the distribution version, commit, build time, BuildID, and
`build_kind=release`. Local or unstamped builds remain development builds and use
their isolated default Store.

## Unified Control and Store upgrades

The unified Session protocol removes Application enrollment, connection leases,
credentials and routes. Bot and TUI use the same Host principal and permissions.
Old clients must upgrade; there is no permanent forwarding protocol. Applications
retain identity, Notebook, business tools and scheduling through explicit profiles,
MCP and Skills. See [Session profiles](session-profiles.md).

The isolated `control/internal/storemigration/applicationv3` importer upgrades
schema 1/2 to 3 atomically. It preserves Session/call/resource identities, bytes,
original requests and receipt provenance. Old operation namespaces map to inert,
retained common receipts; pending calls are cancelled and uncertain effects stay
unknown. The importer never redispatches old intent. It can be removed when the
minimum supported Store is schema 3; archived evidence has separate retention.

Older writers reject schema 3. Rollback requires a protected complete pre-upgrade
Store backup, not an executable downgrade. Stop the Host and prevent client
restarts while taking the backup; include database WAL/SHM files, configuration
and credentials, and verify restoration in isolation. Reconcile post-upgrade
writes before restoring a backup.

Managed launch upgrades early Hosts automatically through the bounded
pre-v0.72.2 transition. Running Session turns temporarily preserve the old Host;
idle Sessions, detached Tasks and resident Bot/TUI subscriptions do not. New
Hosts retain atomic admission fencing; the early-version transition uses a live
activity read followed by shutdown. See [replacement](control-client-schema.md#managed-host-replacement). Installation alone does not
prove that the selected Host has changed.

CWD, inheritance, execution and permissions remain bound at Session creation.
Model, reasoning effort, supported service tier, instructions and tools can
change at the next unissued model request, including within a Turn. A provider's
support for priority is not a promise that every model supports Fast. Synthetic
provider checks cannot establish real-model or Fast service behavior. Release
notes must distinguish actual model requests from deterministic recovery tests
and limit live acceptance to the exercised models, adapter, and request paths;
a successful priority request does not establish latency, billing, or GUI and
distribution acceptance.

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

The tag workflow verifies that the tagged commit belongs to `main`, then builds,
signs, notarizes, and publishes artifacts. It relies on the protected branch
rather than querying historical CI runs or repeating ordinary tests.
Maintainers must tag a reviewed commit on `main` without bypassing its required checks. Release workflows are
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
   `R2_ENDPOINT` are available and the public release domain is active. Confirm
   the `default` environment can use the organization
   `APPLE_DEVELOPER_ID_P12_BASE64`, `APPLE_DEVELOPER_ID_P12_PASSWORD`,
   `APPLE_NOTARY_APPLE_ID`, and `APPLE_NOTARY_PASSWORD` secrets plus the
   `APPLE_TEAM_ID` variable, without same-named repository or environment
   overrides. Keep certificate and password material out of this repository.
5. Submit the intended changes through PRs and wait for their selected checks.
   Review the resulting Release PR's version and changelog, and wait for its
   metadata validation. Merging this PR is the decision to publish; there is no
   separate CI approval step. Do not rerun unchanged local gates just for a tag.
6. Ensure the release notes are concise and user-visible. When retiring a durable writer,
   record the last writer and first no-write version; retain its compatibility
   reader until the supported upgrade floor reaches that version.

Run optional architecture, SDK, protocol, race, regression, proxy, documentation,
or dry-run checks only when the release changes those boundaries. See
[Testing](testing.md).

## Publish

Merge the reviewed Release PR after `quality` passes when ready to publish. The
bot creates the root `vX.Y.Z` tag at that merge commit and a GitHub Release containing the
changelog. That tag triggers `release.yml`: GoReleaser builds unpublished archives;
the macOS job signs and notarizes Darwin; the publication job uploads final
archives and checksums while preserving the bot's release notes. The GitHub
Release may be visible before all distribution steps have finished; complete
the acceptance checks below before announcing availability.

After verified GitHub assets are uploaded, the workflow publishes the six
platform npm packages concurrently in the same OIDC-enabled job. All six must
settle and pass registry integrity verification before the separate main-package
step runs. A failed platform does not cancel its siblings, but blocks the main
package and R2. R2 mirrors the archives under `releases/vX.Y.Z/` and updates
`latest.txt` last. GitHub Releases remains the complete versioned archive.

Publication can resume after a partial failure. The GitHub step compares each
existing asset's SHA256 digest with the verified archive before skipping it; a
different digest stops the job without replacing the asset. Each npm package is
packed once per attempt, and an existing version is skipped only when its
registry SHA512 integrity matches the staged package. A different package at
the same version stops publication. Upload acceptance is not registry readiness:
each package gets up to 20 minutes of readback with exponential backoff (5 to
30 seconds, with jitter). Preflight reads retry transient failures for up to two
minutes; only an explicit 404 permits an upload. Incomplete metadata, timeouts,
network errors, HTTP 429 and 5xx are retried; authentication/read-policy errors
and conflicting SHA512 values are not treated as absence. Each read is bounded
by 20 seconds and the remaining deadline, with npm's nested fetch retries off.

A failed or timed-out upload can still have reached npm. Except for explicit
authentication/usage failures, the publisher reconciles the original package
against its packed SHA512 without issuing another upload. Expired readback
leaves publication unconfirmed and blocks downstream steps; inspect the npm
registry (and staged publishing settings if relevant) before recovery. The
65-minute job budget accommodates concurrent platform verification followed by
main-package verification. R2 reads the verified GitHub assets back and checks
their bytes before updating `latest.txt`.

This separates immutable upload from eventually visible metadata, following
[npm's immutable version and integrity contract](https://docs.npmjs.com/cli/v11/commands/npm-publish/)
and [idempotent read retry guidance](https://docs.npmjs.com/cli/v11/using-npm/config/#fetch-retries).
Community workflows such as
[Jodit's registry-readiness gate](https://github.com/xdan/jodit/blob/main/.github/workflows/release.yml)
also wait after `npm publish`. Caelis requires matching integrity before
continuing, including when that wait expires. OIDC/provenance and the original
signed archive gates remain unchanged.

Rerun a failed publication job within the same workflow run so it downloads the
successful notarization job's `verified-release-archives` artifact. A full
workflow rerun may produce different timestamped signatures; if any published
asset differs, publication stops for investigation. Do not overwrite published
assets or re-sign the Darwin binaries to resume a partially published version.

A failed tag run keeps using the script from that immutable tag. A publishing or signing fix
merged later to `main` cannot take effect through **Rerun failed jobs** on the
old run. The one-off `workflow_dispatch` permits one `v0.71.1` publication
resume from the Accepted Apple receipt, exact submission ZIP, and final
release archives saved by signing run `38505541188` (built from tag run
`38420720149`). Its guard
verifies the original tag, signing run and artifact digests, Apple Accepted
receipt and ZIP hash, both Darwin binary bytes, empty release, and absence of
any additional recovery dispatch. Run it on reviewed `main` only after these
conditions hold. It does not rebuild, re-sign, or submit to Apple again. A
failed publication job can be rerun within that same run using the saved final
archives. Keep the original tag and release assets immutable; the normal
release-please tag flow remains the path for new versions. Remove this one-off
dispatch after recovery.

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
   Extract both Darwin CLIs and check their Developer ID team, hardened runtime,
   timestamp, and signature. Compare their hashes with the corresponding npm
   runtime binaries. The notarization job must have an Accepted Apple result and
   matching submission ZIP digest in its log.
2. Verify the same version exists for all six platform npm packages and
   `@caelis/caelis`.
3. Verify `https://releases.caelis.dev/latest.txt`, download the archives and
   checksum file from its versioned R2 prefix, and validate the checksums against
   the GitHub assets. The workflow reads every R2 object back before updating
   `latest.txt`.
4. Verify the root module version is available through the public Go proxy.
