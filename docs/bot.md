# Bot Mode

A **Bot** is a persistent named assistant with one private conversation. Bot mode
is a standalone window over the same Host and canonical Session infrastructure
as the coding TUI. It is a personal Agent independent of any user project.
Private documents and notes are routine tools; explicitly enabled managed work
runs in separate Control-owned Sessions and workspaces.

`caelis bot` launches the Bot TUI. It discovers or attaches to the same managed
Host as `caelis`, so Bots, Sessions, credentials, and configuration live in the
same Store. A Bot conversation needs no project workspace trust, so Bot mode
runs no trust prompt. Managed or remote Hosts keep running after the window
closes; an `-embedded` Host ends with its owning process, exactly as for the
coding TUI.

## Commands

Bot mode exposes Bot chat commands and shared Host configuration. `/help` lists
both. The TUI presents one conversation. Managed work and desktop connections use the
[Bot backend contract](bot-backend.md); ordinary participant and project commands
remain outside this conversation.

| Command | Effect |
| --- | --- |
| `/new` | Create a Bot. Asks for a name, then an optional description; Enter skips the description. |
| `/bots` | Open the Bot selection overlay. |
| `/settings` | Edit the active Bot's name, description, and model. |
| `/model` | Choose a connected provider model for the active Bot. |
| `/connect` | Connect a provider or external ACP Agent to the Host. |
| `/disconnect` | Disconnect a provider or external ACP Agent from the Host. |
| `/team` | Configure shared Host Agent bindings. `/subagent` is an alias. |
| `/theme` | Change the TUI theme. |
| `/quit` | Exit the Bot TUI. `/exit` is an alias. |
| `/help` | Show the Bot command set. |
| `/status` | Show the active Bot's configuration and reply state. |

The Bot selection overlay is also reachable as `/resume`.

Connections and [team configuration](participants.md) are shared Host settings,
not Bot execution capabilities. Configuring an ACP Agent, a collaborator, or the
Memory Steward does not let a Bot invoke them or use Memory. The Host's built-in
Memory remains enabled independently of Bot mode.

The first message a Bot receives is its first Turn; typing plain text chats with
the active Bot. If no Bot is active yet, Bot mode asks you to create or select
one before sending.

## Bots, identity, and ownership

- Creating a Bot from `/new` records a user-supplied name and optional description.
  The name is the only required input. Cancelling the form creates nothing.
- A Bot's identity is stable and is derived from the creating principal and the
  create operation, not from its name or workspace. Renaming a Bot via
  `/settings` keeps the same identity, conversation, and history.
- Bots are owner-scoped. Listing returns only the current principal's Bots, and
  opening or editing one is authorized against the owner of its conversation.
- A Bot conversation is product-owned and hidden from ordinary Session lists and
  resume candidates. Knowing its Session ID does not grant workspace commands:
  a Bot conversation accepts inspection, prompting, Turn cancel, Bot reads and
  edits, and the focused managed-work and client commands. Close, session/config, and participant commands are rejected.
- On startup Bot mode lists your Bots and reconciles: no Bots opens the create
  flow, exactly one opens that Bot, and several open the selection overlay.

## Configuration

`/new` and `/settings` use one form for name, description, and model. Existing
values are prefilled; clearing the description removes it. Tab moves between
fields and the save action. Enter on Model opens the searchable provider picker;
Esc returns to the form with its draft intact. `/model` opens that picker directly.
Left/Right adjusts effort; Tab switches to Fast when supported. Enter applies the
model draft, and Create or Save commits the complete form. Esc closes without
saving. `/bots` supports typing or pasting a search inside its selection overlay.

Every Bot has a private file area and the five bounded file tools. The complete
Bot configuration also carries `managed_work`, `desktop_actions`, and
`work_permission`. The two capability flags default to false; turning them on
requires an explicit Bot configuration save. `work_permission` accepts only
`workspace-write` (also the default when omitted). Unsupported permissions,
reasoning effort, and speed options are rejected. The TUI settings form currently
covers identity and model settings; the backend API exposes the capability flags.
Existing conversations retain false capability flags when omitted. The private
file area is `<Store>/bots/<Bot ID>/files/`; older notebook directories are not
read, copied, or migrated.

- A Bot's model must be an existing provider model from the configured catalog.
  Selection is explicit: Bot mode never silently binds a different provider or a
  non-provider backend. The UI displays the public model selector (for example,
  `deepseek/deepseek-flash`); durable configuration retains the stable model ID.
- Creating a Bot with no model snapshots the Host's current default model when
  one exists. A Bot can still be created and renamed before any provider is
  configured; chatting fails explicitly until a model is selected.
- Creation preserves the selected Host default's reasoning and speed settings.
  Explicit model selection starts from that model's catalog defaults and allows
  editing supported effort and Fast options. Metadata-only edits preserve the
  current model and options, including when its catalog entry is unavailable.
  Control rejects unsupported reasoning or speed settings.
- Saving is refused while a reply is streaming. The save is a compare-and-swap
  on the conversation revision and applies to your next message. Other open Bot
  windows pick up saved settings through their regular Host status refresh;
  refreshing does not change the selected conversation or an unsent draft.
- When the name or description changes, saving appends one canonical user
  message that carries the new settings. This message becomes ordinary
  conversation history. Bot mode never rewrites that history or the fixed system
  prefix, including after the Runtime is released and rebuilt.

## Private files and notes

Tell the Bot a lasting preference or something you want to continue later. It
can choose to retain useful information, but does not write after every message.
For an explicit save, ask “Keep this preference in your notebook.” You can also
ask it to show the saved notes, correct an old fact, or update an unfinished
matter. After reopening, ask it to check its notebook before answering.

Each Bot has `<Store>/bots/<Bot ID>/files/`. Use this area for documents,
drafts, task materials, and a Markdown notebook. `index.md` is the notebook entry
point, seeded once for a new file area. Renaming the Bot, changing startup directories, releasing its
Runtime, or restarting the Host does not change this association. Markdown files
are the notebook's content authority; there is no separate note-index database
and no automatic copy to Workspace Memory. The Bot maintains links from the index
to relevant notes. Direct TUI file browsing and editing are not available.

The file tools are `Read`, `Write`, `Patch`, `Glob`, and `Grep`. Their boundary
confines reading, writing, listing, and searching to that Bot's private area;
paths outside it and symbolic-link access are rejected. Directory discovery skips
symbolic links. Writes stage a private file before replacement. A stale `Write`
revision or a failed `Patch` match returns an error without applying the edit;
tool calls are serialized so their checks and writes cannot interleave. Individual
file writes are atomic on supported local Unix filesystems, not a transaction
across a note and its index. An index update failure must be repaired explicitly.
External filesystem edits are not serialized with Bot tool calls.

Notebook content is never injected into the instruction baseline or refreshed at
the start of every Turn. The fixed guidance names `index.md`; new information
enters only as ordinary tool results when the Bot reads it. Normal continuation
and note edits therefore retain the committed model-request prefix. System
watermark compaction may establish a new context baseline; the fixed notebook
entry point remains discoverable without scanning every file. Missing or
unreadable notes produce tool errors, not an invented memory, and nothing
recreates a missing index while a Bot conversation continues. Ask the Bot to
repair it if necessary.

A tool error is not a successful save. Check the tool result when a save matters;
model choices and verbal confirmations alone are not evidence of persistence.
Revising a note does not erase earlier conversation or tool-result history.

## What a Bot conversation is

A Bot conversation runs on the built-in controller with an explicit tool
allowlist. Enabling managed work adds owned list/read/create/continue/steer/interrupt
tools. Mutations bind the current persisted user request from trusted Control
admission; the model cannot select a source, principal, workspace directory, or
operation ID. Enabling desktop actions permits only registered clock, reminder,
and gesture tools while their authenticated client lease is active.

Substantial work receives an independent Session, private directory, native
execution lifecycle, and applicable workspace-write policy and approvals.
Approval cannot widen its mandatory filesystem or network ceiling. Existing
project adoption is not supported. Project configuration, plugins, global MCP
credentials, and Workspace Memory are not loaded into either Bot runtime.
Workspace trust therefore grants no project configuration authority to managed
work. The shared Host remains available to its other consumers.

The fixed instruction baseline and explicit tool schemas come from the running
Host. File contents and results enter as ordinary evidence through tools, never
as configuration or new user authorization. Upgrading the Host does not enable
managed work or desktop actions. Work handles, request sources, operation anchors,
completion notifications, client activations, and reminder grants are owned by
`control/bot` in the secured Control database. Native Session journals remain the
execution authority; the shared AppServer ledger remains the command dispatcher.

## Leaving, switching, and cancelling

These are distinct:

- **Switch** — selecting another Bot from the `/bots` overlay changes which
  conversation the window shows. It does not cancel the previous Bot's work,
  which continues on the Host.
- **Cancel** — pressing `Esc` while a Turn is running requests an interrupt for
  the active Bot's current Turn. It cancels that Turn and pauses automatic completion reports until the next
  accepted user message. Owned work has a separate exact interrupt command. Inside the Bot
  overlay, `Esc` closes the overlay instead.
- **Quit** — `/quit`, `/exit`, `Ctrl+D`, or two presses of `Ctrl+C` close the Bot
  TUI. Quitting does not cancel an accepted Turn and does not stop the Host.

Reconnecting reuses the ordinary Session path. History is restored from
canonical Session truth, and a prompt whose outcome cannot be proven is not
resent. Closing or restarting the Host does not delete Bot conversations:
canonical history remains. A Turn cancelled during Host shutdown is shown as
interrupted; an execution without a durable terminal result is shown as an
unknown outcome. Neither is automatically resumed. `/status` reports the same
completed, failed, or interrupted reply state during live observation and after
reconnecting.

Desktop connection loss, explicit desktop exit, and Host restart have separate
semantics; see [the backend lifecycle contract](bot-backend.md#exit-and-recovery).

## Related

- [Architecture](architecture.md): ownership and Host boundaries.
- [Participants](participants.md): collaborative coding sessions.
- [External ACP agents](external-acp-agents.md): provider and agent connections.
