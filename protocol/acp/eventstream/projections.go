package eventstream

import "github.com/caelis-labs/caelis/protocol/acp/schema"

// ToolCallFromEnvelope returns the original ACP tool_call payload when env
// carries one.
func ToolCallFromEnvelope(env Envelope) (schema.ToolCall, bool) {
	if env.Kind != KindSessionUpdate {
		return schema.ToolCall{}, false
	}
	update, ok := CloneUpdate(env.Update).(schema.ToolCall)
	return update, ok
}

// ToolCallUpdateFromEnvelope returns the original ACP tool_call_update payload
// when env carries one.
func ToolCallUpdateFromEnvelope(env Envelope) (schema.ToolCallUpdate, bool) {
	if env.Kind != KindSessionUpdate {
		return schema.ToolCallUpdate{}, false
	}
	update, ok := CloneUpdate(env.Update).(schema.ToolCallUpdate)
	return update, ok
}

// PermissionRequestFromEnvelope returns the original ACP request_permission
// payload when env carries one.
func PermissionRequestFromEnvelope(env Envelope) (schema.RequestPermissionRequest, bool) {
	if env.Kind != KindRequestPermission || env.Permission == nil {
		return schema.RequestPermissionRequest{}, false
	}
	cloned := CloneEnvelope(env)
	if cloned.Permission == nil {
		return schema.RequestPermissionRequest{}, false
	}
	return *cloned.Permission, true
}
