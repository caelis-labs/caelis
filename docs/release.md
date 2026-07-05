# Release

This is the standard release checklist. Update this document when the process changes.

1. Confirm the worktree only contains intended changes.
2. Confirm `main` is current with `origin/main`.
3. Confirm `README.md` points to the official site and install scripts:
   `https://caelis.dev`, `https://caelis.dev/install.sh`, and
   `https://caelis.dev/install.ps1`.
4. Confirm the npm trusted publisher for every `@caelis/*` package points to
   `caelis-labs/caelis`, workflow filename `release.yml`, environment
   `default`, and allows `npm publish`.
5. Run `make commit-check`, `make release-dry-run`, then `git diff --check`.
6. Commit and push release-ready code to `main`.
7. Tag the exact published commit: `git tag -a vX.Y.Z -m vX.Y.Z`.
8. Push the tag and verify the workflow, GitHub Release, npm versions, `HEAD`,
   `origin/main`, and `vX.Y.Z^{}`.
