# Desktop World package fixture

`plugin.json`, `mcp.json`, and `skills/desktop-world/SKILL.md` are copied
verbatim from Desktop World commit
`39b2bcf1a5b9460766a7f8162e03d9e46f83d45f` (PR #34, rc.7):

- https://github.com/caelis-labs/desktop-world/blob/39b2bcf1a5b9460766a7f8162e03d9e46f83d45f/packaging/plugin/plugin.json
- https://github.com/caelis-labs/desktop-world/blob/39b2bcf1a5b9460766a7f8162e03d9e46f83d45f/packaging/plugin/mcp.json
- https://github.com/caelis-labs/desktop-world/blob/39b2bcf1a5b9460766a7f8162e03d9e46f83d45f/packaging/plugin/skills/desktop-world/SKILL.md

The upstream package is MIT licensed. The fixture contains no DTW runtime or
desktop access. The deterministic test places its own MCP helper at `bin/dtw`,
with one `exec` tool, to exercise Caelis's local installer, Session snapshots
and process startup. Skill authoring references are unnecessary for that test
and are omitted. This does not prove the native CUA worker, OS permissions,
signing, distribution, or GUI effects. A verified native package can instead
be tested with `CAELIS_DTW_SINGLE_EXEC_ROOT` on a supported platform.
