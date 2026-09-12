# Terminal UI

`/theme` opens a local theme picker. Up/Down previews the selection, Enter
applies it, and Esc restores the previous choice. `/theme nord` applies a
supported theme directly. The command is available during a running turn and
never becomes a Session message or a Control/Agent command.

The selection belongs to the current TUI instance. `CAELIS_THEME` selects the
startup theme; interactive changes do not rewrite environment variables or
persist in the Session.

| Choice | Behavior |
| --- | --- |
| `auto` / `terminal` | Retains the terminal background and adapts the existing terminal palette to its brightness. |
| `catppuccin` | Chooses Mocha or Latte from the terminal's reported brightness. |
| `catppuccin-mocha` / `mocha` | Full dark Catppuccin palette. |
| `catppuccin-latte` / `latte` | Full light Catppuccin palette. |
| `nord` | Full dark Nord palette. |
| `dracula` | Full dark Dracula palette. |

Named community themes paint their background as well as their foregrounds,
including otherwise empty terminal cells. They remain readable when selected
on a terminal with the opposite brightness. Terminals limited to 16 colors,
and `NO_COLOR`, offer only the terminal choice; RGB theme selection falls back
to terminal colors if capabilities change. Existing `dark`, `light`, and
`solarized` environment values remain supported at startup.

Community colors and roles come from the
[Catppuccin style guide](https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md),
[Nord palette](https://www.nordtheme.com/docs/colors-and-palettes/), and
[Dracula specification](https://draculatheme.com/spec).
Syntax roles use the Chroma styles pinned in `go.mod`. Markdown code blocks,
shell command tokens, and rich diff code use the same immutable style per theme.
Colors below the readability threshold receive a brightness correction that
preserves their hue; palette conversion is included in UI contrast checks.
Diff backgrounds blend the theme's insertion/deletion colors over its base.

Rich diffs use two columns when the panel has at least 120 columns and each
side has room for 48 source columns plus its gutter. Smaller panels use one
column. Each side wraps independently, related replacement lines stay aligned,
and changed graphemes receive stronger emphasis. Line numbers and `+`/`-`
markers remain visible in all color modes. Raw `diff / hunk` and `@@` markers
are hidden; a blank row separates nonadjacent hunks. The underlying diff data
retains its source positions.
