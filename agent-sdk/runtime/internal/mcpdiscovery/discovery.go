package mcpdiscovery

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const MetadataKey = "caelis.mcp.discovery.v1"

type contextKey struct{}

// WithEvents carries a detached projection of complete canonical history past
// prompt compaction. This cache has no writer or independent durable authority.
func WithEvents(ctx context.Context, events []*session.Event) context.Context {
	return context.WithValue(ctx, contextKey{}, FromEvents(events))
}
func FromContext(ctx context.Context) map[string]tool.Binding {
	source, _ := ctx.Value(contextKey{}).(map[string]tool.Binding)
	out := map[string]tool.Binding{}
	for k, v := range source {
		out[k] = v
	}
	return out
}
func FromEvents(events []*session.Event) map[string]tool.Binding {
	out := map[string]tool.Binding{}
	for _, event := range events {
		Apply(out, event)
	}
	return out
}

// Apply accepts only new-format successful canonical discovery receipts.
// Old name-only results and Exposure Active/Pending never grant call eligibility.
func Apply(out map[string]tool.Binding, event *session.Event) {
	if event == nil || !session.IsCanonicalHistoryEvent(event) || event.Type != session.EventTypeToolResult || event.Tool == nil || event.Tool.Name != tool.ToolSearchToolName || event.Tool.Status != "completed" {
		return
	}
	admitted, ok := event.Meta[MetadataKey].(bool)
	if !ok || !admitted {
		return
	}
	// The receipt's canonical output owns the binding. The metadata marker
	// distinguishes this admitted format without storing another binding list.
	for _, entry := range tool.ParseToolSearchOutput(event.Tool.Output).Tools {
		b := entry.Binding
		if b != nil && entry.Name == b.Name && entry.InputSchema != nil && b.Name != "" && b.Identity != "" && b.Version != "" {
			out[b.Name] = *b
		}
	}
}
