package tuiapp

import (
	"encoding/json"
	"slices"
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
	failure     string
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
			display := &desktopWorldDisplay{app: desktopWorldAppLabel(app), actions: desktopWorldScriptActions(source)}
			if effects, ok := strings.CutSuffix(detail, " · "+app); ok {
				display.recordEffects(effects)
			}
			return display
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

// Control emits acknowledged DTW operations before the dispatch outcome
// counts in this title. Counts alone cannot identify an operation.
func (d *desktopWorldDisplay) recordEffects(effects string) {
	var acknowledged []desktopWorldAction
	uncertain := false
	for _, part := range strings.Split(effects, ", ") {
		if action, ok := desktopWorldActions[part]; ok && !action.read && len(acknowledged) < 2 {
			acknowledged = append(acknowledged, action)
		}
		for _, outcome := range []string{" unknown", " not dispatched", " interrupted"} {
			uncertain = uncertain || strings.HasSuffix(part, outcome)
		}
	}
	if uncertain || len(acknowledged) > 0 && (len(d.actions) == 0 || d.observation()) {
		d.actions = acknowledged
	}
}

func (d *desktopWorldDisplay) recordProjectedResult(input transcript.ToolProjectionInput, event *transcript.Event) {
	output := transcript.RawMap(input.RawOutput)
	if d.search {
		apps, ok := output["apps"].([]any)
		d.scopedEmpty = ok && len(apps) == 0 && output["next"] == nil && output["error"] == nil
	}
	if output["error"] != nil {
		event.ToolError = true
	}
	if event.ToolError {
		d.failure = desktopWorldFailure(output, event.ToolOutput, event.ToolOutputSynthetic)
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

func desktopWorldFailure(output map[string]any, text string, synthetic bool) string {
	if detail := toolFailureMessage(output["error"]); detail != "" {
		return desktopWorldInline(detail)
	}
	if synthetic {
		return ""
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") {
		return desktopWorldInline(structuredToolFailureMessage(text))
	}
	if text != "failed" && text != "completed" {
		return desktopWorldInline(strings.Split(text, "\n")[0])
	}
	return ""
}

func structuredToolFailureMessage(text string) string {
	var raw map[string]any
	if json.Unmarshal([]byte(text), &raw) == nil {
		return toolFailureMessage(raw["error"])
	}
	return ""
}

func toolFailureMessage(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		text, _ := value["message"].(string)
		code, _ := value["code"].(string)
		return firstNonEmpty(text, code)
	}
	return ""
}

func (d *desktopWorldDisplay) visible(failed bool) bool {
	return failed || d.search || len(d.actions) > 0
}

func (d *desktopWorldDisplay) observation() bool {
	return d.search || len(d.actions) > 0 && d.actions[0].read
}

func (d *desktopWorldDisplay) sameObservation(other *desktopWorldDisplay) bool {
	return d.observation() && d.app == other.app && d.search == other.search && d.query == other.query &&
		d.scopedEmpty == other.scopedEmpty && slices.Equal(d.actions, other.actions)
}

func (d *desktopWorldDisplay) label(width int, failed bool) string {
	if failed {
		if d.search {
			return ansi.Truncate("App search failed", width, "…")
		}
		return desktopWorldTargetLabel("Desktop action failed", "Failed", d.app, width)
	}
	if d.search {
		category := "Find apps"
		if d.scopedEmpty {
			category = "No apps in scope"
		}
		if d.query != "" {
			prefix := category + " for “"
			if room := width - displayColumns(prefix) - 1; room > 1 {
				return prefix + ansi.Truncate(d.query, room, "…") + "”"
			}
		}
		return ansi.Truncate(category, width, "…")
	}
	if len(d.actions) == 0 {
		return ""
	}
	long := desktopWorldSentence(desktopWorldActionList(d.actions, " and ", false))
	short := desktopWorldSentence(desktopWorldActionList(d.actions, ", ", true))
	return desktopWorldTargetLabel(long, short, d.app, width)
}

func desktopWorldTargetLabel(long, short, app string, width int) string {
	if app == "" {
		return ansi.Truncate(long, width, "…")
	}
	const in = " in "
	if title := long + in + app; displayColumns(title) <= width {
		return title
	}
	available := width - displayColumns(in)
	minTarget := minInt(displayColumns(app), 3)
	if available < minTarget+4 {
		return ansi.Truncate(short, width, "…")
	}
	actionWidth := available - minTarget
	detail := strings.NewReplacer("“", "", "”", "", " and ", ", ").Replace(long)
	switch {
	case displayColumns(detail+in+app) <= width:
		return detail + in + app
	case displayColumns(long) <= actionWidth:
		short = long
	case displayColumns(detail) <= actionWidth:
		short = detail
	case displayColumns(short) > actionWidth:
		short = strings.ReplaceAll(short, ", ", "/")
	}
	action := ansi.Truncate(short, actionWidth, "…")
	return action + in + ansi.Truncate(app, available-displayColumns(action), "…")
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
