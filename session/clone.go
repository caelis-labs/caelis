package session

import "time"

// Clone returns a deep copy of the event.
func (e Event) Clone() Event {
	cp := e
	cp.Actor = e.Actor.Clone()
	if e.Scope != nil {
		s := *e.Scope
		cp.Scope = &s
	}
	if e.UserPayload != nil {
		cp.UserPayload = &UserPayload{Parts: cloneParts(e.UserPayload.Parts)}
	}
	if e.AssistantPayload != nil {
		cp.AssistantPayload = &AssistantPayload{Parts: cloneParts(e.AssistantPayload.Parts)}
	}
	if e.SystemPayload != nil {
		cp.SystemPayload = &SystemPayload{Parts: cloneParts(e.SystemPayload.Parts)}
	}
	if e.ToolCallPayload != nil {
		cp.ToolCallPayload = e.ToolCallPayload.Clone()
	}
	if e.ToolResultPayload != nil {
		cp.ToolResultPayload = e.ToolResultPayload.Clone()
	}
	if e.PlanPayload != nil {
		cp.PlanPayload = e.PlanPayload.Clone()
	}
	if e.CompactionPayload != nil {
		v := *e.CompactionPayload
		v.RetainedMessages = cloneCompactionRetainedMessages(e.CompactionPayload.RetainedMessages)
		cp.CompactionPayload = &v
	}
	if e.LifecyclePayload != nil {
		cp.LifecyclePayload = e.LifecyclePayload.Clone()
	}
	if e.NoticePayload != nil {
		cp.NoticePayload = e.NoticePayload.Clone()
	}
	if e.HandoffPayload != nil {
		v := *e.HandoffPayload
		cp.HandoffPayload = &v
	}
	if e.ParticipantPayload != nil {
		cp.ParticipantPayload = e.ParticipantPayload.Clone()
	}
	if e.ProviderMeta != nil {
		cp.ProviderMeta = cloneMap(e.ProviderMeta)
	}
	return cp
}

// Clone returns a deep copy of the actor ref.
func (a ActorRef) Clone() ActorRef {
	return a // all fields are value types
}

// Clone returns a deep copy of the tool call payload.
func (p *ToolCallPayload) Clone() *ToolCallPayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Args != nil {
		cp.Args = cloneMap(p.Args)
	}
	if p.Display != nil {
		cp.Display = cloneParts(p.Display)
	}
	if p.Truncation != nil {
		v := *p.Truncation
		cp.Truncation = &v
	}
	return &cp
}

// Clone returns a deep copy of the tool result payload.
func (p *ToolResultPayload) Clone() *ToolResultPayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Content != nil {
		cp.Content = cloneParts(p.Content)
	}
	if p.Display != nil {
		cp.Display = cloneParts(p.Display)
	}
	if p.Truncation != nil {
		v := *p.Truncation
		cp.Truncation = &v
	}
	return &cp
}

// Clone returns a deep copy of the plan payload.
func (p *PlanPayload) Clone() *PlanPayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Entries != nil {
		cp.Entries = make([]PlanEntry, len(p.Entries))
		copy(cp.Entries, p.Entries)
	}
	return &cp
}

// Clone returns a deep copy of the lifecycle payload.
func (p *LifecyclePayload) Clone() *LifecyclePayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Details != nil {
		cp.Details = cloneMap(p.Details)
	}
	return &cp
}

// Clone returns a deep copy of the notice payload.
func (p *NoticePayload) Clone() *NoticePayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Details != nil {
		cp.Details = cloneMap(p.Details)
	}
	return &cp
}

// cloneParts returns a deep copy of EventParts.
func cloneParts(parts []EventPart) []EventPart {
	if parts == nil {
		return nil
	}
	cp := make([]EventPart, len(parts))
	for i, p := range parts {
		cp[i] = p.Clone()
	}
	return cp
}

func cloneCompactionRetainedMessages(messages []CompactionRetainedMessage) []CompactionRetainedMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]CompactionRetainedMessage, len(messages))
	for i, msg := range messages {
		out[i] = CompactionRetainedMessage{
			Role:  msg.Role,
			Parts: cloneParts(msg.Parts),
		}
	}
	return out
}

// Clone returns a deep copy of the event part.
func (p EventPart) Clone() EventPart {
	cp := p
	if p.ToolUse != nil {
		v := *p.ToolUse
		if v.Args != nil {
			v.Args = cloneMap(v.Args)
		}
		cp.ToolUse = &v
	}
	if p.ToolResultRef != nil {
		v := *p.ToolResultRef
		cp.ToolResultRef = &v
	}
	if p.Media != nil {
		v := *p.Media
		if v.Data != nil {
			v.Data = make([]byte, len(v.Data))
			copy(v.Data, p.Media.Data)
		}
		cp.Media = &v
	}
	if p.FileRef != nil {
		v := *p.FileRef
		cp.FileRef = &v
	}
	if p.ProviderMeta != nil {
		cp.ProviderMeta = cloneMap(p.ProviderMeta)
	}
	return cp
}

// cloneMap returns a shallow clone of a map.
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// CloneSession returns a deep copy of the session.
func (s Session) Clone() Session {
	cp := s
	cp.Workspace = s.Workspace.Clone()
	cp.State = s.State.Clone()
	if s.Participants != nil {
		cp.Participants = make([]ParticipantBinding, len(s.Participants))
		for i, p := range s.Participants {
			cp.Participants[i] = p.Clone()
		}
	}
	return cp
}

// Clone returns a deep copy of the workspace.
func (w Workspace) Clone() Workspace {
	cp := w
	if w.Context != nil {
		cp.Context = make(map[string]string, len(w.Context))
		for k, v := range w.Context {
			cp.Context[k] = v
		}
	}
	return cp
}

// Clone returns a deep copy of the participant binding.
func (p ParticipantBinding) Clone() ParticipantBinding {
	cp := p
	if p.Metadata != nil {
		cp.Metadata = make(map[string]string, len(p.Metadata))
		for k, v := range p.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

// Clone returns a deep copy of the participant payload.
func (p *ParticipantPayload) Clone() *ParticipantPayload {
	if p == nil {
		return nil
	}
	cp := *p
	if p.Metadata != nil {
		cp.Metadata = make(map[string]string, len(p.Metadata))
		for k, v := range p.Metadata {
			cp.Metadata[k] = v
		}
	}
	return &cp
}

// Ensure time is used for any timestamp operations.
var _ = time.Now
