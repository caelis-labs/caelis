package tuiapp

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/charmbracelet/x/ansi"
)

const desktopWorldDisplayKey = "tui.desktop_world"

// A Desktop World display is local to the TUI projection. Wire metadata can
// contain arbitrary strings or maps, but cannot populate this private type.
type desktopWorldDisplay struct {
	app         string
	search      bool
	query       string
	actions     []desktopWorldAction
	scopedEmpty bool
}

func desktopWorldProjection(env eventstream.Envelope) bool {
	if env.Delivery == nil || env.Position == nil || env.Position.Durable == nil {
		return false
	}
	// External ACP tool updates are transient or mirrors. Native nested calls
	// are durable mirrors with the actual Code Mode parent in the Envelope.
	return env.Delivery.Mode == eventstream.DeliveryCanonical ||
		env.Delivery.Mode == eventstream.DeliveryMirror && env.ParentTool != nil &&
			env.ParentTool.ToolName == "js_exec" && env.ParentTool.ToolCallID != ""
}

func projectDesktopWorldDisplay(input transcript.ToolProjectionInput) *desktopWorldDisplay {
	// The existing Control projector emits these titles only for the verified
	// Desktop World binding on native tool facts. Ordinary names, titles on
	// untrusted deliveries, and ACP display metadata carry no binding evidence.
	name := input.ToolName
	switch name {
	case "dtw__apps", "desktop_world__apps":
		if input.ToolTitle == name+" · Apps" {
			query, _ := input.RawInput["query"].(string)
			return &desktopWorldDisplay{search: true, query: desktopWorldInline(query)}
		}
	case "dtw__exec", "desktop_world__exec":
		app, _ := input.RawInput["app"].(string)
		source, _ := input.RawInput["source"].(string)
		detail, ok := strings.CutPrefix(input.ToolTitle, name+" · ")
		if ok && app != "" && (detail == app || strings.HasSuffix(detail, " · "+app)) {
			return &desktopWorldDisplay{app: desktopWorldAppLabel(app), actions: desktopWorldScriptActions(source)}
		}
	}
	return nil
}

func desktopWorldAppLabel(app string) string {
	if i := strings.LastIndexAny(app, `/\`); i >= 0 && i < len(app)-1 {
		app = app[i+1:]
		for _, ext := range []string{".exe", ".app"} {
			if strings.HasSuffix(strings.ToLower(app), ext) && len(app) > len(ext) {
				app = app[:len(app)-len(ext)]
				break
			}
		}
	}
	return desktopWorldInline(app)
}

func desktopWorldInline(text string) string {
	return strings.Join(strings.Fields(sanitizeRenderableText(text)), " ")
}

func (d *desktopWorldDisplay) recordProjectedResult(input transcript.ToolProjectionInput, event *transcript.Event) {
	output := transcript.RawMap(input.RawOutput)
	if d.search {
		apps, ok := output["apps"].([]any)
		d.scopedEmpty = ok && len(apps) == 0 && output["next"] == nil && output["error"] == nil
	} else if output["error"] != nil {
		d.actions = nil
	}
	// Preserve the original output for disclosure, including replies whose
	// ACP content only carries a generic status.
	text := strings.TrimSpace(event.ToolOutput)
	if len(output) > 0 && (event.ToolOutputSynthetic || text == "" || text == "completed" || text == "failed") {
		if raw, err := json.MarshalIndent(output, "", "  "); err == nil {
			event.ToolOutput = string(raw)
			event.ToolOutputSynthetic = false
		}
	}
}

func (d *desktopWorldDisplay) label(width int, failed bool, done bool) string {
	if d.search {
		if d.scopedEmpty && !failed {
			const result = "No apps in scope"
			const prefix = " for “"
			if room := width - displayColumns(result+prefix) - 1; d.query != "" && room > 1 {
				return result + prefix + ansi.Truncate(d.query, room, "…") + "”"
			}
			return ansi.Truncate(result, width, "…")
		}
		if d.query == "" {
			return ansi.Truncate("Find apps", width, "…")
		}
		long := "Find apps matching “" + d.query + "”"
		if displayColumns(long) <= width {
			return long
		}
		const prefix = "Find apps “"
		if room := width - displayColumns(prefix) - 1; room > 0 {
			return prefix + ansi.Truncate(d.query, room, "…") + "”"
		}
		return ansi.Truncate("Find apps", width, "…")
	}
	long, medium, short := "Run a script", "", "Run script"
	if failed {
		long, short = "Script failed", "Failed"
	} else if len(d.actions) > 0 {
		long = desktopWorldActionList(d.actions, " and ", false)
		medium = desktopWorldActionList(d.actions, ", ", false)
		short = desktopWorldActionList(d.actions, ", ", true)
		// Source describes intent even after the enclosing tool settles.
		if done {
			long, medium, short = "Plan to "+long, "To "+medium, "To "+short
		} else {
			long, medium, short = desktopWorldSentence(long), desktopWorldSentence(medium), desktopWorldSentence(short)
		}
	}
	if d.app == "" {
		return ansi.Truncate(long, width, "…")
	}
	if title := d.app + " · " + long; displayColumns(title) <= width {
		return title
	}
	if title := d.app + " · " + medium; medium != "" && displayColumns(title) <= width {
		return title
	}
	if done && !failed && len(d.actions) > 0 {
		// Keep literal action details when the full sentence is just too
		// wide. The short lead-in still identifies these as intended steps.
		intent := desktopWorldCompactIntent(d.actions)
		room := width - displayColumns(": "+intent)
		if room >= minInt(3, displayColumns(d.app)) {
			return ansi.Truncate(d.app, room, "…") + ": " + intent
		}
	}
	available := width - displayColumns(" · ")
	if available < 6 {
		return ansi.Truncate(short, width, "…")
	}
	if first, _, more := strings.Cut(short, ", "); more && displayColumns(short) > available-3 {
		short = first + "…"
	}
	actionWidth := minInt(displayColumns(short), available-3)
	return ansi.Truncate(d.app, available-actionWidth, "…") + " · " + ansi.Truncate(short, actionWidth, "…")
}

func desktopWorldCompactIntent(actions []desktopWorldAction) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		phrase := action.phrase
		if before, target, ok := strings.Cut(phrase, "“"); ok {
			phrase = before + strings.TrimSuffix(target, "”")
		}
		parts = append(parts, phrase)
	}
	return "To " + strings.Join(parts, ", ")
}

func desktopWorldActionList(actions []desktopWorldAction, separator string, compact bool) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		phrase := action.phrase
		if compact {
			phrase = action.short
		}
		parts = append(parts, phrase)
	}
	return strings.Join(parts, separator)
}

func desktopWorldSentence(line string) string {
	return strings.ToUpper(line[:1]) + line[1:]
}
