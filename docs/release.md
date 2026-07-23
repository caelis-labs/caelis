# Release

All Go packages, including `agent-sdk/*`, share the root module, root
`vX.Y.Z` tag, and release lifecycle. A release publishes six CLI archives,
checksums, and the corresponding npm packages.

## Gate model

Ordinary quality runs once for each pull request and `main` SHA:

- formatting, lint, vet, architecture and SDK boundary checks;
- generated Control client protocol and maintained-document link checks;
- all Go tests and builds;
- native Windows persistence and credential tests.

Pull requests additionally run the focused Agent SDK race suite. The tag
workflow does not call `quality.yml` or rerun tests, race detection, regression
groups, proxy smoke, or a snapshot. It waits for the existing `main` push
quality run with the exact tag commit SHA, then starts GoReleaser and publishes
the npm packages from that output.

This is an intentional trust boundary: correctness is established before the
tag through the ordinary change workflow. Release automation owns artifact
construction and publication, not a second quality policy. Its wait job is only
coordination: a successful quality run unlocks artifacts, while failure,
cancellation, or a missing exact-SHA run prevents the release job from starting.
PR quality remains an earlier signal; it may use a temporary merge SHA and is
therefore not the release identity.

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
5. Review a curated behavioral summary rather than using raw commit subjects as
   the release note.

`make sdk-race`, `make regression`, `make sdk-proxy-smoke`, and
`make release-dry-run` remain available for a change that specifically needs
them. They are diagnostic tools, not unconditional release stages.

## Publish

Create an annotated tag for the quality-approved SHA and push it:

```bash
git tag -a vX.Y.Z -m vX.Y.Z
git push origin vX.Y.Z
```

The release workflow starts on the pushed tag, waits for successful exact-SHA
`main` quality, and then runs its artifact job. It publishes platform npm
packages only after GoReleaser succeeds, and publishes the main npm package
last.

## Post-publish acceptance

Before declaring the release complete:

1. Verify `origin/main`, the annotated tag, and the workflow commit resolve to
   the intended SHA, and both quality and release workflows succeeded.
2. Verify the public GitHub Release contains Darwin, Linux, and Windows
   amd64/arm64 archives plus `checksums.txt`.
3. Download one archive, verify its checksum, and confirm `caelis version`
   reports the tagged version and commit.
4. From a clean external module, consume
   `github.com/caelis-labs/caelis@vX.Y.Z` through the Go proxy without a local
   `replace` and compile every package in `agent-sdk/supported-packages.txt`.
5. Verify all seven npm packages report `X.Y.Z`, the main package pins all
   optional platform dependencies to that version, and its installed CLI
   reports the tagged commit.
6. Record the release SHA, workflow URLs, public asset checks, and SDK
   compatibility decision in the GitHub Release or linked workflow run, not in
   a repository-local evidence document.
