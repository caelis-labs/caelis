# Bot Mode

A **Bot** is a persistent named assistant with one private conversation. Bot mode
is a standalone window over the same Host and canonical Session infrastructure
as the coding TUI, with execution limited to chat and a private notebook.

`caelis bot` launches the Bot TUI. It discovers or attaches to the same managed
Host as `caelis`, so Bots, Sessions, credentials, and configuration live in the
same Store. A Bot conversation needs no project workspace trust, so Bot mode
runs no trust prompt. Managed or remote Hosts keep running after the window
closes; an `-embedded` Host ends with its owning process, exactly as for the
coding TUI.

## Commands

Bot mode exposes Bot chat commands and shared Host configuration. `/help` lists
both. Workspace execution, participant prompts, Memory, and Worker commands are
not part of Bot mode.

| Command | Effect |
| --- | --- |
| `/new` | Create a Bot. Asks for a name, then an optional description; Enter skips the description. |
| `/bots` | Open the Bot selection overlay. |
| `/settings` | Edit the active Bot's name, description, model, and notebook. |
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
  a Bot conversation accepts only inspection, prompting, Turn cancel, and Bot
  reads and edits. Close, session/config, and participant commands are rejected.
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

New Bots have a private notebook. Bots created with the tool-free baseline stay
tool-free until you enable it: open `/settings`, move to Notebook, press Enter
to select **Enable**, then move to **Save** and press Enter. Esc discards the draft.
Enabling retains canonical history and the current conversation's model-visible
history, including any existing compaction checkpoint. It starts a new request
baseline with notebook instructions and tools, so the old request-prefix cache
is not preserved. This is a one-way capability change: ordinary conversation,
renaming, model selection, and Runtime reactivation never enable a legacy Bot or
disable an enabled notebook. The enable event and capability commit atomically;
if saving fails or its outcome is unknown, refresh settings before trying a new
operation. Other windows and later Host starts read the same durable capability.

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

## Private notebook

Tell the Bot a lasting preference or something you want to continue later. It
can choose to retain useful information, but does not write after every message.
For an explicit save, ask “Keep this preference in your notebook.” You can also
ask it to show the saved notes, correct an old fact, or update an unfinished
matter. After reopening, ask it to check its notebook before answering.

Each Bot has `<Store>/bots/<Bot ID>/notebook/`, with `index.md` as the stable
entry point. Renaming the Bot, changing startup directories, releasing its
Runtime, or restarting the Host does not change this association. Markdown files
are the notebook's content authority; there is no separate note-index database
and no automatic copy to Workspace Memory. The Bot maintains links from the index
to relevant notes. Direct TUI file browsing and editing are not available.

Only `Read`, `Write`, `Patch`, `Glob`, and `Grep` are admitted. The actual file
boundary confines reading, writing, listing, and searching to that Bot's notebook;
paths outside it and symbolic-link access are rejected. Directory discovery skips
symbolic links. Writes stage a private file before replacement. A stale `Write`
revision or a failed `Patch` match returns an error without applying the edit;
tool calls are serialized so their checks and writes cannot interleave. Individual
file writes are atomic on supported local Unix filesystems, not a transaction
across a note and its index. An index update failure must be repaired explicitly.
External filesystem edits are not serialized with Bot tool calls.

Notebook content is never injected into the system prefix or refreshed at the
start of every Turn. The fixed guidance names `index.md`; new information enters
only as ordinary tool results when the Bot reads it. Normal continuation and
note edits therefore retain the committed model-request prefix. System watermark
compaction may establish a new context baseline; the fixed notebook entry point
remains discoverable without scanning every file. Missing or unreadable notes
produce tool errors, not an invented memory, and Runtime activation never silently
recreates a missing index. Ask the Bot to repair it if necessary.

A tool error is not a successful save. Check the tool result when a save matters;
model choices and verbal confirmations alone are not evidence of persistence.
Revising a note does not erase earlier conversation or tool-result history.

## What a Bot conversation is

A Bot conversation runs on the built-in controller. Its fixed instruction/tool
baseline changes only at the explicit notebook enable boundary described above.
Notebook text is evidence, not configuration or higher-priority instructions; it
cannot change the user-maintained name, description, or permissions. The Bot has
no shell, arbitrary workspace access, Workspace Memory, plugin installation,
collaboration, or background Worker capability. Its canonical Session cannot carry
a Workspace Memory binding.

## Leaving, switching, and cancelling

These are distinct:

- **Switch** — selecting another Bot from the `/bots` overlay changes which
  conversation the window shows. It does not cancel the previous Bot's work,
  which continues on the Host.
- **Cancel** — pressing `Esc` while a Turn is running requests an interrupt for
  the active Bot's current Turn. It cancels only that Turn. Inside the Bot
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

## Related

- [Architecture](architecture.md): ownership and Host boundaries.
- [Participants](participants.md): collaborative coding sessions.
- [External ACP agents](external-acp-agents.md): provider and agent connections.
