package tuiapp

import (
	"encoding/json"
	"slices"
	"strconv"
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
	input       map[string]any
	effects     desktopWorldEffects
	scopedEmpty bool
	hasResult   bool
}

type desktopWorldEffects struct {
	sent       int
	incomplete bool
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
			return &desktopWorldDisplay{search: true, query: desktopWorldInline(query), input: input.RawInput}
		}
	case "dtw__exec", "desktop_world__exec":
		app, _ := input.RawInput["app"].(string)
		source, _ := input.RawInput["source"].(string)
		detail, ok := strings.CutPrefix(input.ToolTitle, name+" · ")
		if ok && app != "" && (detail == app || strings.HasSuffix(detail, " · "+app)) {
			var effects desktopWorldEffects
			if detail != app {
				effects = desktopWorldProjectedEffects(strings.TrimSuffix(detail, " · "+app))
			}
			return &desktopWorldDisplay{app: desktopWorldAppLabel(app), actions: desktopWorldScriptActions(source), input: input.RawInput, effects: effects}
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

// Control's existing binding-verified title carries ledger totals even when
// the nested call's structured result is absent from the ACP update.
func desktopWorldProjectedEffects(detail string) desktopWorldEffects {
	var effects desktopWorldEffects
	for field := range strings.SplitSeq(detail, ", ") {
		number, outcome, _ := strings.Cut(field, " ")
		count, err := strconv.Atoi(number)
		if err != nil || count <= 0 {
			continue
		}
		switch outcome {
		case "dispatched":
			effects.sent = count
		case "not dispatched", "unknown", "interrupted":
			effects.incomplete = true
		}
	}
	return effects
}

func (d *desktopWorldDisplay) recordResult(output map[string]any) bool {
	if d.search {
		apps, ok := output["apps"].([]any)
		if !ok || output["error"] != nil {
			return false
		}
		d.scopedEmpty = len(apps) == 0 && output["next"] == nil
	} else {
		id, _ := output["execution_id"].(string)
		if id == "" && output["error"] == nil {
			return false
		}
		d.effects.sent = max(d.effects.sent, transcript.MetaInt(output, "effects", "dispatch", "dispatched"))
		d.effects.incomplete = d.effects.incomplete || output["error"] != nil ||
			transcript.MetaInt(output, "effects", "interrupted") > 0 ||
			transcript.MetaInt(output, "effects", "dispatch", "unknown") > 0 ||
			transcript.MetaInt(output, "effects", "dispatch", "not_dispatched") > 0
	}
	d.hasResult = true
	return true
}

func (d *desktopWorldDisplay) recordProjectedResult(input transcript.ToolProjectionInput, event *transcript.Event) {
	output := transcript.RawMap(input.RawOutput)
	d.recordResult(output)
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

func desktopWorldWithReturnedResult(child, script SubagentEvent) SubagentEvent {
	if child.desktopWorld == nil || child.desktopWorld.hasResult || !child.Done || child.Err ||
		!script.Done || script.Err || script.ScriptCompletion == "failed" || script.OutputSynthetic {
		return child
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(script.Output), &output); err != nil ||
		!desktopWorldReturnsCall(script.FullArgs, child.Name, child.desktopWorld.input) {
		return child
	}
	display := *child.desktopWorld
	if display.recordResult(output) {
		child.desktopWorld = &display
		if child.OutputSynthetic || !renderableTextHasContent(child.Output) {
			child.Output = script.Output
			child.OutputSynthetic = false
		}
	}
	return child
}

func (d *desktopWorldDisplay) preview(done bool) []desktopWorldAction {
	inputs := 0
	conditionalInput, conditionalRead := false, false
	for _, action := range d.actions {
		if !action.read {
			inputs++
			conditionalInput = conditionalInput || action.conditional
		} else if action.conditional {
			conditionalRead = true
		}
	}
	readsOnly := inputs == 0
	fallbackRead := false
	if done {
		if d.effects.incomplete {
			return nil
		}
		if inputs > 0 && d.effects.sent < inputs {
			// Counts cannot associate a partial dispatch with source sites.
			// A possible read branch can still describe a zero-dispatch reply.
			fallbackRead = d.effects.sent == 0 && conditionalRead
			if !fallbackRead {
				return nil
			}
			readsOnly = true
		}
	}
	var chosen []desktopWorldAction
	for _, action := range d.actions {
		if action.read != readsOnly || fallbackRead && !action.conditional {
			continue
		}
		// A branch or repeated site can account for several ledger entries.
		// The totals do not confirm other sites on such a path.
		if done && !action.read && conditionalInput {
			action.conditional = true
		}
		if slices.Contains(chosen, action) {
			continue
		}
		chosen = append(chosen, action)
		if len(chosen) == 2 {
			break
		}
	}
	return chosen
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
	} else if actions := d.preview(done); len(actions) > 0 {
		long = desktopWorldActionList(actions, " and ", false)
		medium = desktopWorldActionList(actions, ", ", false)
		short = desktopWorldActionList(actions, ", ", true)
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
	available := width - displayColumns(" · ")
	if available < 6 {
		return ansi.Truncate(short, width, "…")
	}
	actionWidth := minInt(displayColumns(short), available-3)
	return ansi.Truncate(d.app, available-actionWidth, "…") + " · " + ansi.Truncate(short, actionWidth, "…")
}

func desktopWorldActionList(actions []desktopWorldAction, separator string, compact bool) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		phrase := action.phrase
		if compact {
			phrase = action.short
		}
		if action.conditional {
			phrase = "may " + phrase
		}
		parts = append(parts, phrase)
	}
	line := strings.Join(parts, separator)
	return strings.ToUpper(line[:1]) + line[1:]
}
