# @caelis/caelis

Caelis is a collaboration workspace for AI agents. Its built-in runtime, native
collaborators, and external ACP agents participate in the same Session-scoped
network with shared mailbox and messaging semantics.

See the [project README](https://github.com/caelis-labs/caelis#readme) or
[中文说明](https://github.com/caelis-labs/caelis/blob/main/README.zh-CN.md) for
setup and usage. This package installs the `caelis` CLI from npm.

## Install

```bash
npm i -g @caelis/caelis
```

Supported platforms: macOS/Linux/Windows (`x64`, `arm64`).

or run without global install:

```bash
npx @caelis/caelis --help
```

## How it works

This package installs a platform-specific `caelis` binary from npm optional dependencies.

This keeps installation traffic on the npm registry path instead of fetching binaries from GitHub Releases during `postinstall`.
