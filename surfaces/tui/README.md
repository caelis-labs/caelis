# Terminal UI

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
