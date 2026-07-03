# Release

This is the standard release checklist. Update this document when the process changes.

1. Confirm the worktree only contains intended changes.
2. Confirm `main` is current with `origin/main`.
3. Confirm the npm trusted publisher for every `@caelis/*` package points to `caelis-labs/caelis`, workflow filename `release.yml`, environment `default`, and allows `npm publish`.
4. Run `make release-dry-run`, then `git diff --check`.
5. Commit and push release-ready code to `main`.
6. Tag the exact published commit: `git tag -a vX.Y.Z -m vX.Y.Z`.
7. Push the tag and verify the workflow, GitHub Release, npm versions, `HEAD`, `origin/main`, and `vX.Y.Z^{}`.
