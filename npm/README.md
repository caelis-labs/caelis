# @onslaughtsnail/caelis

Install `caelis` from npm.

## Install

```bash
npm i -g @onslaughtsnail/caelis
```

Supported platforms: macOS/Linux (`x64`, `arm64`).

or run without global install:

```bash
npx @onslaughtsnail/caelis --help
```

## How it works

This package installs a platform-specific `caelis` binary from npm optional dependencies.

This keeps installation traffic on the npm registry path instead of fetching binaries from GitHub Releases during `postinstall`.
