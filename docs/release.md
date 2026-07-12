# Release

This is the standard release and post-publish verification procedure. Update it
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
   Every regression group must print at least one matched test; `[no tests to
   run]` or an empty selector is a failure.
5. Review a curated behavioral release summary. Do not use raw commit subjects
   as the only release note: an intermediate feature may have been removed
   before the tag.
6. Commit and push the release-ready SHA to `main`.
7. Wait for the quality workflow for that exact SHA to succeed before creating
   the tag. This operator check catches branch-only failures early; the release
   workflow independently invokes the same reusable quality workflow for the
   tag SHA before publish.
8. Run `go run ./scripts/sdk_api_compat -print-baseline` and confirm it is the
   immediately preceding reachable release. On a candidate tag, the resolver
   skips the candidate itself. Remove waivers that become stale when the
   baseline rolls forward; each remaining waiver must bind an exact package and
   declaration digest with a concrete reason.

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
3. The release workflow calls `quality.yml` as a reusable workflow at the
   caller's tag SHA. The publish job declares `needs: quality`, so no GoReleaser
   or npm publish step can start unless every quality gate succeeds for that
   exact candidate. The tag name is passed to the no-replace consumer smoke.

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
   clean external module without a local `replace` directive. Compile every
   import in `agent-sdk/supported-packages.txt` in that consumer.
6. Verify npm reports `X.Y.Z` for:
   - `@caelis/caelis`;
   - `@caelis/caelis-darwin-arm64` and `-darwin-x64`;
   - `@caelis/caelis-linux-arm64` and `-linux-x64`;
   - `@caelis/caelis-windows-arm64` and `-windows-x64`.
7. Verify the main npm package pins every optional platform dependency to the
   same version and its installed CLI reports the expected version/commit.
8. Record the release SHA, workflow URLs, asset check, npm/Go-proxy evidence,
   and any SDK compatibility decision in the GitHub Release or linked workflow
   run. Do not create a repository-local release evidence document.

## Required CI Evidence

Release gating should record, rather than merely assume, these results:

- formatting, lint, vet, architecture lint, SDK boundary, tests, and build;
- focused Agent SDK `-race` coverage;
- regression suite;
- documentation-link validation;
- six-target release snapshot or equivalent build coverage;
- no-replace consumer smoke against the actual tag/Go proxy.

The reusable quality workflow records the focused Agent SDK race suite,
regression suite, maintained-document link validation, and clean external Go
consumer as named steps in addition to the ordinary quality gates. Pull request
and `main` runs compile the current-source quickstart and use the rolling prior
release for the tagged-artifact smoke; a tag release supplies its own candidate
tag. The tagged gate extracts that tag's fixture/allowlist and forbids replace,
so current API additions are not compiled against an old fixture. The link gate covers `README.md`,
`agent-sdk/README.md`, and maintained Markdown under `docs/` while ignoring
example links embedded in vendored skill text.

Local notes are useful diagnostic context, but they are not a substitute for
publish-gated CI evidence.
