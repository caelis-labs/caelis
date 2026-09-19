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

// Call dispatches source-bound tools. Controller creation additionally requires
// an authenticated grant bound to the exact current controller epoch.
func (s *Service) Call(ctx context.Context, i Identity, req Request) (json.RawMessage, error) {
	if req.Tool == "StartThread" {
		return s.startThread(ctx, i, req.Arguments)
	}
	if (req.Tool == "ReadThread" || req.Tool == "WaitThread") && i.Member != "parent" {
		return nil, errors.New("controller tool is unavailable to participants")
	}
	var args struct {
		To             string   `json:"to"`
		Message        string   `json:"message"`
		ReplyTo        string   `json:"reply_to"`
		Handle         string   `json:"handle"`
		Cursor         *uint64  `json:"cursor"`
		Limit          int      `json:"limit"`
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
	case "ReadMessages":
		result, err = s.ReadMessages(ctx, i, args.Cursor, args.Limit)
	case "ReadThread":
		result, err = s.Read(ctx, i, Target{Handle: args.Handle, After: args.After})
	case "SendMessage":
		var sent Message
		sent, err = s.Send(ctx, i, args.To, args.Message, args.ReplyTo)
		if err == nil {
			// Sending is already committed. A separate mailbox failure must not
			// turn this acknowledgement into a failed send that invites a retry.
			mail, _ := s.Receive(ctx, i)
			result = toolSendResult{ID: sent.ID, Status: "queued", Messages: messageToolViews(mail)}
		}
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
	return json.Marshal(toolResultView(result))
}

// Definitions selects the tool set from the Control-owned caller role. Children
// can discover peers and exchange mail; only controllers observe or wait on work.
func Definitions(controller bool) []tool.Definition {
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": append([]string{}, required...), "additionalProperties": false}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": description}
	}
	definitions := []tool.Definition{
		{Name: "ReadMessages", Description: "Read one unread page of explicit Session group messages, including directed mail. Each member has independent progress. Does not consume directed mail or start work. A lost response can be replayed with an earlier cursor; explicit cursor reads do not change unread progress. Retention gaps are reported.", InputSchema: object(map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 128}, "cursor": map[string]any{"type": "integer", "minimum": 0}}), EffectClass: tool.EffectNonIdempotent},
		{Name: "ListThreads", Description: "List participants and their Session-scoped handles.", InputSchema: object(map[string]any{}), EffectClass: tool.EffectReadOnly},
		{Name: "SendMessage", Description: "Queue mail, then independently take your pending mail. Success means queued, not delivered; do not resend. Delivery occurs at a supported input boundary or through a SendMessage reply. Use received IDs as reply_to.", InputSchema: object(map[string]any{"to": text("Recipient handle from ListThreads."), "message": text("Message body."), "reply_to": map[string]any{"type": "string", "minLength": 36, "maxLength": 36, "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", "description": "Optional UUID of the message being answered."}}, "to", "message"), EffectClass: tool.EffectNonIdempotent},
	}
	if !controller {
		return definitions
	}
	return append(definitions, []tool.Definition{
		{Name: "ReadThread", Description: "Read a participant's latest public result and status. Pass cursor as after to omit unchanged output; full history is not returned.", InputSchema: object(map[string]any{"handle": text("Participant handle from ListThreads."), "after": map[string]any{"type": "integer", "minimum": 0}}, "handle"), EffectClass: tool.EffectReadOnly},
		{Name: "WaitThread", Description: "Wait for mail, new input, or selected threads to finish or need attention. Returned mail is removed; already admitted input is not repeated. Pass each cursor as after to suppress repeated results. Timeout leaves work running.", InputSchema: object(map[string]any{"threads": map[string]any{"type": "array", "maxItems": 8, "items": object(map[string]any{"handle": text("Participant handle."), "after": map[string]any{"type": "integer", "minimum": 0}}, "handle")}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 60}}), EffectClass: tool.EffectNonIdempotent},
	}...)
}

// Tools binds the native tool surface to the same invocation contract as MCP.
func Tools(controller bool, invoke Invoke) []tool.Tool {
	definitions := Definitions(controller)
	out := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
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
