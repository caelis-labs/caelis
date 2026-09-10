package collaboration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Request is shared by native tools and the authenticated MCP bridge.
type Request struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// Invoke calls a source-bound collaboration service.
type Invoke func(context.Context, Request) (json.RawMessage, error)

// Call executes only the participant tool surface. Creation is intentionally
// absent: a participant credential cannot create another Agent.
func (s *Service) Call(ctx context.Context, i Identity, req Request) (json.RawMessage, error) {
	var args struct {
		To             string   `json:"to"`
		Message        string   `json:"message"`
		ReplyTo        string   `json:"reply_to"`
		Handle         string   `json:"handle"`
		After          uint64   `json:"after"`
		Targets        []Target `json:"threads"`
		TimeoutSeconds *int     `json:"timeout_seconds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("expected one arguments object")
	}
	var result any
	var err error
	switch req.Tool {
	case "ListThreads":
		result, err = s.List(ctx, i)
	case "ReadThread":
		result, err = s.Read(ctx, i, Target{Handle: args.Handle, After: args.After})
	case "SendMessage":
		result, err = s.Send(ctx, i, args.To, args.Message, args.ReplyTo)
	case "ReceiveMessages":
		result, err = s.Receive(ctx, i)
	case "WaitThread":
		seconds := 30
		if args.TimeoutSeconds != nil {
			seconds = *args.TimeoutSeconds
		}
		result, err = s.WaitThreads(ctx, i, args.Targets, time.Duration(seconds)*time.Second)
	default:
		return nil, errors.New("unknown collaboration tool")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// Definitions is the identical model-visible schema for native and MCP tools.
func Definitions() []tool.Definition {
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": append([]string{}, required...), "additionalProperties": false}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	return []tool.Definition{
		{Name: "ListThreads", Description: "Discover the participants in this work Session. Addresses are scoped to this Session.", InputSchema: object(map[string]any{}), EffectClass: tool.EffectReadOnly},
		{Name: "SendMessage", Description: "Put a message in another participant's mailbox. Success confirms queuing only. Delivery is automatic at a safe boundary during a running turn when steering is supported, or on the next turn otherwise. The recipient can also take pending mail through its collaboration tools. Do not repeat a successful send.", InputSchema: object(map[string]any{"to": text("Recipient handle from ListThreads."), "message": text("Message body."), "reply_to": map[string]any{"type": "string", "minLength": 36, "maxLength": 36, "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", "description": "Optional UUID of the message being answered."}}, "to", "message"), EffectClass: tool.EffectNonIdempotent},
		{Name: "ReceiveMessages", Description: "Take pending messages from your own mailbox. Taken messages are removed. Reply using SendMessage and the received message ID as reply_to.", InputSchema: object(map[string]any{}), EffectClass: tool.EffectNonIdempotent},
		{Name: "ReadThread", Description: "Read a participant's latest public result and status. Supply the returned cursor as after to suppress previously observed output. This is a latest-result snapshot, not full conversation history.", InputSchema: object(map[string]any{"handle": text("Participant handle from ListThreads."), "after": map[string]any{"type": "integer", "minimum": 0}}, "handle"), EffectClass: tool.EffectReadOnly},
		{Name: "WaitThread", Description: "Wait for incoming mailbox messages or for selected threads to finish or need attention. Messages returned are removed; thread observations are repeatable using cursors. Timeout does not cancel work.", InputSchema: object(map[string]any{"threads": map[string]any{"type": "array", "maxItems": 8, "items": object(map[string]any{"handle": text("Participant handle."), "after": map[string]any{"type": "integer", "minimum": 0}}, "handle")}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 60}}), EffectClass: tool.EffectNonIdempotent},
	}
}

// Tools binds the native tool surface to the same invocation contract as MCP.
func Tools(invoke Invoke) []tool.Tool {
	out := make([]tool.Tool, 0, 4)
	for _, definition := range Definitions() {
		out = append(out, boundTool{definition: definition, invoke: invoke})
	}
	return out
}

type boundTool struct {
	definition tool.Definition
	invoke     Invoke
}

func (t boundTool) Definition() tool.Definition { return tool.CloneDefinition(t.definition) }
func (t boundTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	result, err := t.invoke(ctx, Request{Tool: t.definition.Name, Arguments: call.Input})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{ID: call.ID, Name: t.definition.Name, Content: []model.Part{model.NewTextPart(string(result))}}, nil
}
