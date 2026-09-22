package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/bot"
)

func (r *botTurnResolver) desktopTools(ctx context.Context, admission *botTurnAdmission) []tool.Tool {
	if admission == nil || admission.source.ClientID == "" {
		return nil
	}
	source := admission.source
	client, err := r.composition.authorities.botWork.ActiveClient(ctx, source.PrincipalID, source.BotID, source.ClientID)
	if err != nil {
		return nil
	}
	return r.desktopToolsFor(admission, client.Actions)
}

// desktopToolsFor also supplies the maximal permitted schema set for prompt
// budgeting; a nil admission has no dispatch authority.
func (r *botTurnResolver) desktopToolsFor(admission *botTurnAdmission, actions []string) []tool.Tool {
	var tools []tool.Tool
	for _, action := range actions {
		name := map[string]string{"clock": "DesktopClock", "reminders": "DesktopReminders", "gesture": "DesktopGesture"}[action]
		properties := map[string]any{}
		required := []string{}
		str := func() map[string]any { return map[string]any{"type": "string"} }
		switch action {
		case "clock":
		case "gesture":
			properties["gesture"] = map[string]any{"type": "string", "enum": []string{"attention", "nod", "celebrate"}}
			required = append(required, "gesture")
		case "reminders":
			properties["operation"] = map[string]any{"type": "string", "enum": []string{"list", "save", "remove"}}
			required = append(required, "operation")
			for _, key := range []string{"id", "label", "prompt", "at", "daily", "time_zone"} {
				properties[key] = str()
			}
			properties["every_minutes"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 10080}
		default:
			continue
		}
		tools = append(tools, tool.NamedTool{Def: tool.Definition{Name: name, Description: "Use the authenticated desktop client's " + action + " capability. Reminders are resident schedules owned by the desktop; save requires a stable id, label, prompt and exactly one schedule (RFC3339 at, every_minutes, or HH:MM daily with time_zone). Only a completed native receipt proves success. This tool grants no command, project or global plugin authority.", EffectClass: tool.EffectIdempotent, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			if admission == nil {
				return tool.Result{}, errors.New("desktop action requires an authenticated request")
			}
			select {
			case <-admission.ready:
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
			var args bot.DesktopArguments
			decoder := json.NewDecoder(strings.NewReader(string(call.Input)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&args); err != nil {
				return tool.Result{}, err
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return tool.Result{}, errors.New("desktop action requires one JSON object")
			}
			store := r.composition.authorities.botWork
			request, err := store.QueueDesktop(ctx, admission.source, call.ID, action, args)
			if err != nil {
				return tool.Result{}, err
			}
			waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			result, err := store.AwaitDesktop(waitCtx, request.ID)
			if err != nil {
				return tool.Result{}, err
			}
			data, err := json.Marshal(result)
			return tool.Result{IsError: result.IsError || result.State != "completed", Content: []model.Part{model.NewTextPart(string(data))}}, err
		}})
	}
	return tools
}
