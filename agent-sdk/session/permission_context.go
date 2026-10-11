package session

import "encoding/json"

// PermissionContext preserves an endpoint's independent permission prompt and
// exact structured evidence without inventing a tool invocation. RawSubject and
// NativeRequestID are private evidence; Control publishes only bounded Subject
// facts and the original options, using its own approval admission identity.
type PermissionContext struct {
	Title           string          `json:"title"`
	Description     *string         `json:"description,omitempty"`
	RawSubject      json.RawMessage `json:"raw_subject,omitempty"`
	NativeRequestID json.RawMessage `json:"native_request_id,omitempty"`
	// DisplaySubjectHint is untrusted remote presentation context. It never
	// replaces RawSubject, canonical Subject, or automatic review evidence.
	DisplaySubjectHint *ApprovalSubject `json:"display_subject_hint,omitempty"`
}

// ClonePermissionContext detaches private endpoint evidence without interpreting
// or rewriting its JSON representation.
func ClonePermissionContext(in *PermissionContext) *PermissionContext {
	if in == nil {
		return nil
	}
	out := *in
	if in.Description != nil {
		out.Description = new(*in.Description)
	}
	out.RawSubject = append(json.RawMessage(nil), in.RawSubject...)
	out.NativeRequestID = append(json.RawMessage(nil), in.NativeRequestID...)
	out.DisplaySubjectHint = CloneApprovalSubject(in.DisplaySubjectHint)
	return &out
}
