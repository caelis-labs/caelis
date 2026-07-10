# Release

This is the standard release and post-publish acceptance checklist. Update it
when the process changes.

All Go packages, including `agent-sdk/*`, are versioned and released with the
root `github.com/caelis-labs/caelis` module and root `vX.Y.Z` tag. There is no
separate Agent SDK module or release process.

## Preflight

1. Confirm the worktree contains only intended changes and `main` is current
   with `origin/main`.
2. Confirm `README.md` points to `https://caelis.dev`,
   `https://caelis.dev/install.sh`, and `https://caelis.dev/install.ps1`.
3. Confirm the npm trusted publisher for every `@caelis/*` package points to
   `caelis-labs/caelis`, workflow `release.yml`, environment `default`, and may
   publish.
4. Run `make commit-check`, the focused Agent SDK race suite,
   `make regression`, `make release-dry-run`, and `git diff --check`.
5. Review a curated behavioral release summary. Do not use raw commit subjects
   as the only release note: an intermediate feature may have been removed
   before the tag.
6. Commit and push the release-ready SHA to `main`.
7. Wait for the quality workflow for that exact SHA to succeed before creating
   the tag.

The focused race suite for SDK persistence/runtime work is:

```bash
go test -race ./agent-sdk/policy/... ./agent-sdk/session/... ./agent-sdk/runtime/...
```

## Publish

1. Tag the exact quality-approved commit:

   ```bash
   git tag -a vX.Y.Z -m vX.Y.Z
   git push origin vX.Y.Z
   ```

2. The release workflow must publish from the dereferenced tag commit, build
   all six supported targets, upload checksums, then publish the six platform
   npm packages before the main npm package.
3. Publishing must be conditional on quality success for the same SHA. Until
   the workflow enforces that dependency itself, waiting before tag creation is
   a mandatory operator gate. The missing automated dependency was recorded by
   the [v0.25.0 acceptance review](agent-sdk-v0.25.0-acceptance.md).

## Post-publish Acceptance

Verify all of the following before declaring the release complete:

1. `HEAD`, `origin/main`, the remote tag, and the dereferenced annotated tag
   resolve to the intended SHA.
2. The quality and release workflows both succeeded for that SHA.
3. The GitHub Release is neither draft nor prerelease and contains:
   - Darwin amd64 and arm64 archives;
   - Linux amd64 and arm64 archives;
   - Windows amd64 and arm64 archives;
   - `checksums.txt`.
4. Download at least one archive, verify its checksum, and run `caelis version`
   to confirm both version and commit.
5. Verify the Go proxy can consume `github.com/caelis-labs/caelis@vX.Y.Z` from a
   clean external module without a local `replace` directive. Compile the 16
   supported SDK imports in that consumer.
6. Verify npm reports `X.Y.Z` for:
   - `@caelis/caelis`;
   - `@caelis/caelis-darwin-arm64` and `-darwin-x64`;
   - `@caelis/caelis-linux-arm64` and `-linux-x64`;
   - `@caelis/caelis-windows-arm64` and `-windows-x64`.
7. Verify the main npm package pins every optional platform dependency to the
   same version and its installed CLI reports the expected version/commit.
8. Record the release SHA, workflow URLs, asset check, npm/Go-proxy evidence,
   and any SDK readiness decision in a versioned acceptance note.

## Required CI Evidence

Release gating should record, rather than merely assume, these results:

- formatting, lint, vet, architecture lint, SDK boundary, tests, and build;
- focused Agent SDK `-race` coverage;
- regression suite;
- documentation-link validation;
- six-target release snapshot or equivalent build coverage;
- no-replace consumer smoke against the actual tag/Go proxy.

A local checklist statement is useful diagnostic context, but it is not a
substitute for publish-gated CI evidence.
