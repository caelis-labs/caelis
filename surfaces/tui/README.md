# Terminal UI

`/model` opens a picker above the fixed composer. Type in the composer to search
configured model names and selectors, use Up/Down to select a row, and Left/Right
to adjust its reasoning effort. For models supporting Fast mode, Tab switches
between effort and Fast. The current model is marked with a filled dot. Enter
or a row click applies the selection; Esc discards all unconfirmed changes.
Model capabilities and the effective selection come from Control. Unknown
capabilities are not inferred from model names. Explicit commands such as
`/model <model> <effort> [fast]` remain available.

`/team` opens a centered configuration overlay. Type to search roles, models or
binding sets. Up/Down selects a row; Enter or a row click opens or confirms it.
The model picker starts on the role's current binding, marked with a filled dot;
Left/Right adjusts effort without saving. Enter saves the binding and returns to
the original role and search; Esc discards the binding draft. New roles remain
local drafts until Create role is confirmed. Tab/Shift+Tab moves between form
fields, and Enter advances a text field. Ctrl+N creates a role, Ctrl+P opens
binding sets, Ctrl+S saves a set, and Delete opens deletion confirmation. Ctrl+W
clears a list search. Saving blocks further edits until the Host responds;
failures retain the draft for correction or retry. The same overlay configures
the connected Host in Bot mode. See [Participants](../../docs/participants.md)
for role, system-agent and binding-set semantics.

`/connect` uses a multi-step overlay above the fixed composer. Search stays
inside the overlay; Esc returns to the previous step with its draft intact, and
Esc at the first step or the close button dismisses the flow. Endpoint and key
share a form, keys are masked, and changing an endpoint clears the entered key.
Tab moves between fields; Left/Right changes a choice. Known models support
Space or click to select several, then Enter connects the selection. Custom
models collect the required capabilities in one form. A sole ACP launch method
is selected automatically. Authentication and model discovery remain owned by
Control. Cancelled discovery cannot reopen the overlay; uncertain save outcomes
require checking `/model` before reconnecting. Success closes the overlay and
preserves the composer draft. Credentials never enter composer history.

`/disconnect` shares this overlay: choose Provider or ACP Agent, search and
select targets with Space or click, then Enter disconnects the selection. Back
preserves selections across searches. `/plugin` uses the same navigation for
installation, removal and marketplace actions; installation sources are entered
in a form. Enable/disable uses the shared multi-select prompt. Completed commands
leave the composer clear; full commands with arguments remain available.

`/resume` and Bot mode's `/bots` search inside their overlays. Search survives
catalog refreshes and never changes the composer draft. Up/Down selects, Enter
opens, and Esc closes. Bot `/new`, `/settings` and `/model` use the shared form
and model controls through the Bot client; see [Bot Mode](../../docs/bot.md).

`/theme` opens a centered local theme picker. Up/Down and clicks update the
selection immediately; after a short pause, the latest selection is previewed
without closing the picker. Mouse hover only highlights a row. Enter applies the
selected theme even if its preview is pending, and Esc restores the previous
choice. Pending previews cannot change the theme after the picker closes.
`/theme nord` applies a supported theme directly. The command is available during
a running turn and never becomes a Session message or an Agent command.

## Saved preferences

Theme and split-pane preferences share the connected Host's `config.json.ui`:

```json
{
  "ui": {
    "theme": "catppuccin",
    "subagent_layout": "right",
    "horizontal_ratio": 55,
    "vertical_ratio": 50
  }
}
```

Startup uses a nonempty `CAELIS_THEME` override, then saved `ui.theme`, then
`auto`. Interactive confirmation changes the current TUI and saves the canonical
selected name, not resolved colors or an automatic Catppuccin flavour. Enter and
`/theme <name>` save; navigation, mouse previews, Esc, and terminal capability
fallbacks do not. An unknown saved name uses `auto` without rewriting the value.
Interactive changes never rewrite environment variables, so an explicit override
still takes precedence on the next launch.

The TUI owns palette interpretation; the Host merges only confirmed fields and
atomically saves them alongside other product configuration. A save failure is
shown in the TUI; the local choice remains active but may not survive restart.
Preferences are scoped to the connected Host/Store, including remote attachment,
not a separate client-local file. Other TUIs load the saved defaults at startup;
already-running instances do not synchronize live. Other Surfaces may ignore
`ui`, and these preferences never enter Agent settings, Session history, or model
context.

## Palettes

The picker lists theme names and keyboard controls without displacing the
transcript or composer. Automatic choices follow the terminal background, not the
OS appearance setting directly.

| Picker label | Command / environment name | Behavior |
| --- | --- | --- |
| Terminal | `auto` / `terminal` | Keeps the terminal background and adapts Caelis colors to its brightness. |
| Catppuccin | `catppuccin` | Uses Mocha on dark terminals and Latte on light terminals. |
| Catppuccin Latte | `catppuccin-latte` / `latte` | Fixed light Catppuccin palette. |
| Catppuccin Mocha | `catppuccin-mocha` / `mocha` | Fixed dark Catppuccin palette. |
| Dracula | `dracula` | Fixed dark Dracula palette. |
| Nord | `nord` | Fixed dark Nord palette. |

Named community themes paint their background as well as their foregrounds,
including otherwise empty terminal cells. The composer has a distinct surface
from the page background. Named themes remain readable when selected on a
terminal with the opposite brightness. Terminals limited to 16 colors,
and `NO_COLOR`, offer only the terminal choice; RGB theme selection falls back
to terminal colors if capabilities change. Existing `dark`, `light`, and
`solarized` environment values remain supported at startup.

Community colors and roles come from the
[Catppuccin style guide](https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md),
[Nord palette](https://www.nordtheme.com/docs/colors-and-palettes/), and
[Dracula specification](https://draculatheme.com/spec).
Syntax roles use the Chroma styles pinned in `go.mod`. Markdown code blocks,
shell command tokens, and rich diff code use the same immutable style per theme.
Colors below the readability threshold receive a brightness correction;
palette conversion is included in UI contrast checks. Diff backgrounds blend
the theme's insertion/deletion colors over its base, with separate strengths
for whole lines and changed text. Red and green strengths are tuned per palette
rather than using equal opacity. Diff syntax and line numbers are checked on
their final background; readable token colors remain unchanged.

In transcripts, rich diff panels start at the tool header's text column, after
the status bullet. They use two columns when the inset panel has at least 120
columns and each side has room for 48 source columns plus its gutter. Smaller
panels use one column. Each side wraps independently, related replacement lines stay aligned,
and changed graphemes receive stronger emphasis. Line numbers and `+`/`-`
markers remain visible in all color modes, and wrapped continuations retain
their syntax and changed-text styles. A single-file diff omits its body title
when the tool header already identifies it; multi-file diffs retain per-file
titles. Raw `diff / hunk` and `@@` markers are hidden; a blank row separates
nonadjacent hunks. The underlying diff data retains its source positions.
