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

Trusted external installers can use the local headless entry:

```sh
caelis plugin install /absolute/path/to/extracted-plugin --json
caelis plugin status desktop-world --json
caelis plugin remove desktop-world --json
```

Flags may precede or follow the directory/ID. `--store-dir <directory>` (or
`CAELIS_STORE_DIR`) selects an isolated Store. By default these commands discover
or start the same managed local Host as the TUI. `--embedded` requests an
exclusive in-process Host and fails if another Host owns the Store. These are
local commands: unset `CAELIS_CONTROL_URL`; no remote install, marketplace,
download, shell script or desktop permission flow is exposed by this entry.
No model configuration or Session creation is required. Exiting the command
leaves the managed Host running.

`install` requires an absolute existing directory and registers it in place;
the installer owns the package files and must keep them available. A first
installation enables the plugin; standard local packages retain the user's
enabled/disabled choice on repeat installs and upgrades. For both Agent Plugins
and Claude packages, the manifest name is the stable installed ID; version,
platform, spaces, or non-English characters in the extraction directory do not
change it.
Repeat installs and upgrades preserve unrelated plugins, marketplaces, Host
settings, explicit executable paths and private `PLUGIN_DATA`. Standard local
updates stay bound to the registered canonical directory, resolved at install
time. Another directory, manifest format or managed registration with the same
ID is an error, even if its manifest claims the same publisher. To switch source
directories, explicitly remove the registration before installing the new root.
Older standard registrations that saved a directory alias instead of a canonical
root require explicit removal and reinstallation. Their saved manifest cannot
prove the original package boundary, including when it resolves to a nested file.
Missing or contradictory saved source evidence also requires reinstallation.
This new registration starts enabled; private data survives by ID, while any
plugin-specific Host executable settings must be configured again. Verify
package provenance before invoking installation.

Older local Claude packages may be registered under their extraction directory
name. Remove that old ID before installing the same package under its manifest
name; Caelis rejects duplicate registrations of one source. Keep the package
directory available and configure any plugin-specific Host executable settings
again after reinstalling.

`remove` unregisters the ID for later activations; repeating it when absent
succeeds. It leaves externally owned files and private data available. `status`
reads the Host registration snapshot and returns `installed:false`
for an absent ID without activating a Session or starting MCP. An enabled
registration and its Host-scoped MCP fields do not prove the tool catalog or
worker readiness of an activated Session. Start a fresh Session
for changed Skill/MCP configuration; active Runtime snapshots remain fixed
until release. Changes to files in place can still change bytes read by an active
process; finish active work before replacing an installer's package files.

JSON stdout contains one object with `schema_version:"1"`, `action`, `id`,
`installed`, and, when registered, `plugin`. The `plugin` snapshot includes
`id`, `root`, `version`, `enabled`, Skills and Host-scoped MCP fields. For example:

```json
{"schema_version":"1","action":"status","id":"desktop-world","installed":false}
```

Success exits zero. Explicit installation and configuration reject malformed
manifests with an actionable error. A previously registered plugin whose
manifest later becomes malformed is omitted from future Runtime assembly;
`status` reports its error state and warning while the Host remains available.
Invalid arguments, ownership conflicts, and unavailable Stores/Hosts exit
nonzero and include `error` in JSON output.
Mutation failures also include the Host `receipt` when available. Check its
`outcome`: `committed` means the mutation took effect even if subsequent
observation or cleanup failed; do not blindly replay an uncertain mutation.
Without `--json`, the command prints a brief text result and errors on stderr.

For Desktop World, verify the package for your OS/CPU and register its extracted
Agent Plugin root. The rewrite package declares `bin/dtw mcp` in `.mcp.json`
and bundles its Skill. No separate Host adapter, CUA SDK, or Node installation
is required. The Skill appears as `desktop-world:desktop-world`; its four MCP
tools are qualified as `desktop_world__apps`, `desktop_world__api`,
`desktop_world__exec`, and `desktop_world__result` inside Caelis.

An operation supplies only `exec({app, source})`: a stable platform app ID and a
script for that app. Caelis asks to allow that app for the current Session on
its first use. Later scripts for the same app reuse that approval; another app
or another Session requires approval again. Missing, empty, or non-string app
arguments are rejected before dispatch. Concurrent first calls for the same
app share the pending approval. Cancelling or denying it does not execute the
pending scripts. Grants remain bound to the accepted plugin/tool source, so a
changed source or schema also requires approval again. There is no permanent
or whole-plugin desktop grant.

For the configured `desktop-world` plugin, `apps`, `api`, and `result` skip the
ordinary MCP source prompt: they discover capabilities or read this connection's
existing execution results. Other tools retain ordinary MCP source approval.
`desktop_world` is not a plugin-ID alias, and an ordinary MCP server named
`desktop-world` does not inherit these rules. Explicit per-call policy Ask/Deny
and OS permissions still apply. Caelis owns approval; DTW receives the same
standard MCP arguments and remains usable with other Agents. Disabling/removing
the plugin excludes it from later Session assembly.

You can copy this instruction to an Agent:

> Verify the plugin publisher, version, and checksum. Extract the matching
> official package outside my workspace. Install the extracted root with
> `caelis plugin install <absolute path> --json`, then start a new Session. Check
> `caelis plugin status desktop-world --json` and the ready MCP tools before using the Skill. Do not
> rewrite the package's MCP configuration, copy credentials, or replay an uncertain input.

## Paths, ownership, and results

For standard packages, `command` is a bare executable name or a `./` path
inside the plugin root. `args`, `env`, and `cwd` expand only `${PLUGIN_ROOT}`
and `${PLUGIN_DATA}` once. `PLUGIN_DATA` is private to the plugin identity under
the Caelis Store, remains across package updates, and is created at MCP startup.
It is not the workspace. The standard stdio process receives essential platform
variables plus package values and explicitly configured Host executable paths;
it does not inherit arbitrary Bot tokens or process environment variables.
Standard Skill metadata is parsed as YAML; the fixed `skills/` location may
link to files elsewhere inside the same plugin package, but never outside it.
Automatic discovery omits a malformed Skill without listing it or emitting a
startup notice; valid Skills in the same package remain available. Explicit
Skill reads still report the parse error. A malformed plugin MCP entry is
omitted without disabling valid sibling servers or Skills; a failed connection
is reported through its MCP status and does not delay Host readiness.
On Windows, environment names are matched without case: an explicit Host
executable path replaces any package value with the same name in another case.
On Unix, differently cased names remain distinct.
Claude values expand `${CLAUDE_PLUGIN_ROOT}`; `${CLAUDE_PROJECT_DIR}` resolves
to the exact Session workspace only when that value is used. Relative Claude
`command` paths, including paths written with backslashes, stay within the
plugin root after symlinks are resolved. An explicit absolute command retains
its declared path. Claude stdio servers receive `CLAUDE_PLUGIN_ROOT` in their
minimal process environment. A malformed Claude server entry is skipped while
other entries remain available. Plugin files do not replace the separately trusted
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
