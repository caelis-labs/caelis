# Plugin integration

Caelis accepts [Agent Plugins 1.0](https://agent-plugins.org/specification)
packages with root `plugin.json`, optional root `mcp.json`, and `skills/`.
It also accepts Claude plugins with `.claude-plugin/plugin.json`, root
`.mcp.json`, and an inline `mcpServers` map. Both formats use the existing
Plugin manager, MCP client, Skill catalog, and ToolSearch. A root standard
manifest takes precedence over legacy manifests in the same package; there is
only one copy of each declared server. For a Claude package, an inline server
entry overrides the root `.mcp.json` entry with the same name. An existing
native Caelis manifest takes precedence on a duplicate server name.

## Install and use

Verify the publisher and package checksum, extract the package outside your
workspace, then enter this in Caelis's TUI:

```text
/plugin install /absolute/path/to/extracted-plugin
```

The plugin is enabled when installed. Use `/plugin manage` to enable or disable
it later. Start a new Session/task for configuration changes to take effect;
an active Session retains its MCP and Skill snapshot. No plugin files or
credentials are copied into the project workspace.

For Desktop World, use the verified **Full** package for your OS and CPU.
Its `mcp.json` starts the bundled Node launcher; Caelis neither selects Node
nor starts a second Desktop World bridge. The **Lite** package requires the
trusted Host to set `ExecutableEnv["DTW_NODE_PATH"]` to an absolute Node 24.x
executable for that plugin. The TUI does not yet expose this advanced Host
setting, so use Full through `/plugin install`. Desktop World input still
requires its normal app grant and OS permissions. ToolSearch can discover
`desktop_exec` and `desktop_status`; reviewing a script remains the caller's
responsibility.

You can copy this instruction to an Agent:

> Verify the plugin publisher, version, and checksum. Extract the matching
> official Full archive outside my workspace. In Caelis, install the extracted
> root with `/plugin install <absolute path>`, then start a new Session. Check
> `/plugin manage` and the ready MCP tools before using the Skill. Do not
> rewrite the package's `mcp.json`, copy credentials, or replay a failed input.

## Paths, ownership, and results

For standard packages, `command` is a bare executable name or a `./` path
inside the plugin root. `args`, `env`, and `cwd` expand only `${PLUGIN_ROOT}`
and `${PLUGIN_DATA}` once. `PLUGIN_DATA` is private to the plugin identity under
the Caelis Store, remains across package updates, and is created at MCP startup.
It is not the workspace. The standard stdio process receives essential platform
variables plus package values and explicitly configured Host executable paths;
it does not inherit arbitrary Bot tokens or process environment variables.
On Windows, environment names are matched without case: an explicit Host
executable path replaces any package value with the same name in another case.
On Unix, differently cased names remain distinct.
Claude values expand `${CLAUDE_PLUGIN_ROOT}`; `${CLAUDE_PROJECT_DIR}` resolves
to the exact Session workspace only when that value is used. Relative Claude
`command` paths, including paths written with backslashes, stay within the
plugin root after symlinks are resolved. An explicit absolute command retains
its declared path. Plugin files do not replace the separately trusted
workspace `.mcp.json` overlay.
Project overlays still require trust of that exact canonical workspace and
retain their existing priority and Session snapshot behavior.

MCP tool results keep ordered text, validated PNG/JPEG image bytes as inline
base64 media, bounded resource-link metadata, bounded embedded text, and
bounded `structuredContent` (including native action receipts). `isError` and
the original tool call ID are retained through canonical history and model
replay. Images over 8 MiB, over 20 million pixels, malformed, or of another
MIME type are omitted with an explicit error. Audio and embedded binary blobs
are unsupported and reported as such. Resource links are metadata only;
Caelis does not fetch them as an unapproved side effect. Image delivery to a
model depends on that provider's declared image-input capability.

The MCP client remains alive for the Runtime lifetime. A process exit removes
its tools from the ready catalog and reports failed health. Caelis does not
restart the process or retry the last tool call automatically; a new Runtime
may establish a new connection with a new plugin state. Disabling a plugin
affects future Session assembly, not an already running Turn.

## Authentication and scope

Static remote headers remain an explicit Host configuration. The official Go
MCP SDK provides OAuth client helpers, but Core currently lacks a user consent
callback and protected token store at this Host boundary. OAuth client login,
PKCE/discovery, and refresh are therefore deferred; server bearer-token
middleware is not a client login implementation. Bot-owned credentials and
connections remain in Bot and are not inherited by Workers. Claude hooks,
commands, and agents do not gain new execution authority from this format
support. The implementation intentionally adds no Plugin hook runner, separate
tool registry, media-fetch framework, or Desktop World-specific bridge.
