package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func (r *botTurnResolver) workTools(ref session.SessionRef, admission *botTurnAdmission) []tool.Tool {
	var tools []tool.Tool
	for _, name := range []string{"ListWork", "ReadWork", "CreateWork", "ContinueWork", "SteerWork", "InterruptWork"} {
		properties := map[string]any{}
		required := []string{}
		if name != "ListWork" && name != "CreateWork" {
			properties["work_id"] = map[string]any{"type": "string"}
			required = append(required, "work_id")
		}
		if name == "CreateWork" || name == "ContinueWork" || name == "SteerWork" {
			properties["assignment"] = map[string]any{"type": "string", "maxLength": bot.MaxDescriptionBytes}
			required = append(required, "assignment")
		}
		if name == "SteerWork" || name == "InterruptWork" {
			properties["target"] = map[string]any{"type": "object", "properties": map[string]any{"instance_id": map[string]any{"type": "string"}, "session_id": map[string]any{"type": "string"}, "handle_id": map[string]any{"type": "string"}, "run_id": map[string]any{"type": "string"}, "turn_id": map[string]any{"type": "string"}}, "required": []string{"instance_id", "session_id", "handle_id", "run_id", "turn_id"}, "additionalProperties": false}
			required = append(required, "target")
		}
		effect := tool.EffectReadOnly
		if name != "ListWork" && name != "ReadWork" {
			effect = tool.EffectIdempotent
		}
		tools = append(tools, tool.NamedTool{Def: tool.Definition{Name: name, Description: botWorkToolDescriptions[name], EffectClass: effect, Capabilities: tool.Capabilities{ParallelSafe: true}, InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}, Invoke: func(ctx context.Context, call tool.Call) (tool.Result, error) {
			return r.callWorkTool(ctx, ref, admission, name, call)
		}})
	}
	return tools
}

var botWorkToolDescriptions = map[string]string{
	"ListWork":      "List only this Bot's managed work handles. Work IDs are long-lived and are separate from each execution's run and Task IDs.",
	"ReadWork":      "Read one owned work and its exact execution target. Use native Session events to observe progress; never resend an unknown create or continuation with a different operation ID.",
	"CreateWork":    "Delegate substantial work into a new Control-allocated private workspace. Authority is bound to the current authenticated user request, never to assignment text. Returns promptly while work runs independently.",
	"ContinueWork":  "Continue idle owned work in its existing workspace, using the current authenticated user request. An active execution must instead be steered.",
	"SteerWork":     "Forward the current authenticated user request to the exact active owned execution. Assignment text is context and cannot grant permissions.",
	"InterruptWork": "Interrupt the exact active owned execution observed by ReadWork. Never targets another run or closes the Host.",
}

func (r *botTurnResolver) callWorkTool(ctx context.Context, ref session.SessionRef, admission *botTurnAdmission, name string, call tool.Call) (tool.Result, error) {
	active, err := r.composition.sessions.Session(ctx, ref)
	if err != nil {
		return tool.Result{}, err
	}
	id, _ := active.Metadata[bot.MetadataID].(string)
	req := appserver.BotWorkRequest{WriteBase: appserver.WriteBase{SessionID: ref.SessionID}, BotID: id}
	var input struct {
		WorkID     string        `json:"work_id"`
		Assignment string        `json:"assignment"`
		Target     bot.Execution `json:"target"`
	}
	dec := json.NewDecoder(strings.NewReader(string(call.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return tool.Result{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return tool.Result{}, errors.New("bot: tool requires one JSON object")
	}
	req.WorkID = input.WorkID
	req.Assignment = input.Assignment
	req.Target = input.Target
	store := r.composition.authorities.botWork
	var result any
	switch name {
	case "ListWork":
		result, err = store.ListWork(ctx, active.UserID, id)
	case "ReadWork":
		result, err = store.GetWork(ctx, active.UserID, id, input.WorkID)
	default:
		if admission == nil {
			return tool.Result{}, errors.New("bot: work requires a current authenticated user request")
		}
		select {
		case <-admission.ready:
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
		source, sourceErr := store.Request(ctx, active.UserID, id, admission.source.ID)
		if sourceErr != nil {
			return tool.Result{}, sourceErr
		}
		if source.Execution.TurnID == "" {
			return tool.Result{}, errors.New("bot: request admission remains unknown; query its receipt")
		}
		req.SourceID = source.ID
		// Content-derived operations survive a model's retry with a new tool-call
		// ID. The trusted source keeps distinct user requests separate.
		data, _ := json.Marshal(struct {
			Name   string
			Source string
			Input  any
		}{name, source.ID, input})
		sum := sha256.Sum256(data)
		req.OperationID = "bot-tool-" + hex.EncodeToString(sum[:])
		commands := r.composition.authorities.botWorkCommands
		p := appserver.Principal{ID: active.UserID, ClientID: source.ClientID, BotID: id}
		switch name {
		case "CreateWork":
			result, err = commands.CreateBotWork(ctx, p, req)
		case "ContinueWork":
			result, err = commands.ContinueBotWork(ctx, p, req)
		case "SteerWork":
			result, err = commands.SteerBotWork(ctx, p, req)
		case "InterruptWork":
			result, err = commands.CancelBotWork(ctx, p, req)
		}
	}
	if err != nil {
		if receipt, ok := result.(appserver.CommandResult); ok {
			data, marshalErr := json.Marshal(receipt)
			if marshalErr != nil {
				return tool.Result{}, marshalErr
			}
			return tool.Result{IsError: true, Content: []model.Part{model.NewTextPart(string(data)), model.NewTextPart("Recover this operation and its owned work before any further dispatch. " + err.Error())}}, nil
		}
		return tool.Result{}, err
	}
	data, err := json.Marshal(result)
	receipt, isCommand := result.(appserver.CommandResult)
	return tool.Result{IsError: isCommand && receipt.Outcome == appserver.OutcomeUnknown, Content: []model.Part{model.NewTextPart(string(data))}}, err
}
