# Desktop World package fixture

The manifests and Skill follow the rewritten DTW package at
`3e685827b3aa353ba0e48017e86d303e65842401` (PR #70 source head):

- `.claude-plugin/plugin.json` is the same-source manifest with package version `0.2.0`.
- `.mcp.json` launches `bin/dtw mcp` through `${CLAUDE_PLUGIN_ROOT}`.
- `skills/desktop-world/SKILL.md` and `PATTERNS.md` are copied verbatim from that revision.

The upstream package is MIT licensed. This fixture contains no DTW binary or
desktop access. The deterministic test supplies its own MCP helper with
`apps`, `api`, `exec` and `result`, plus a private worker, to verify plugin
installation, Skill discovery, Session snapshots and process startup. It does
not establish native permissions, signing, actual desktop effects or model
usability. Set `CAELIS_DTW_CANDIDATE_ROOT` to an independently verified platform
package to run the native startup gate instead. The separate Control package
protocol test optionally uses `CAELIS_DTW_TEST_APP` to check app state and original
receipt readback without sending desktop input.
