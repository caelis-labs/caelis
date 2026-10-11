package session

import "strings"

// ApprovalFieldState describes the completeness of a decision-context field.
// Opaque means its structure or sensitivity prevents a safe interpretation.
type ApprovalFieldState string

const (
	ApprovalFieldKnown       ApprovalFieldState = "known"
	ApprovalFieldUnavailable ApprovalFieldState = "unavailable"
	ApprovalFieldTruncated   ApprovalFieldState = "truncated"
	ApprovalFieldOpaque      ApprovalFieldState = "opaque"
)

// ApprovalField is bounded display evidence, never execution or grant authority.
// Sensitive fields carry presence only; normalization always removes their text.
type ApprovalField struct {
	State     ApprovalFieldState `json:"state"`
	Text      string             `json:"text,omitempty"`
	Sensitive bool               `json:"sensitive,omitempty"`
}

// ApprovalGrant describes an offered option's scope. OptionID only joins this
// display evidence to the original option; it must never replace that option.
type ApprovalGrant struct {
	OptionID string        `json:"option_id"`
	Scope    ApprovalField `json:"scope"`
}

// ApprovalSubject is an owner-supplied decision-context snapshot. Runtime policy
// supplies known facts, Session preserves them, and Control projects them to all
// clients. Unknown tool inputs stay opaque rather than being parsed by a client.
type ApprovalSubject struct {
	Title            *ApprovalField  `json:"title,omitempty"`
	Description      *ApprovalField  `json:"description,omitempty"`
	Kind             string          `json:"kind"`
	Tool             ApprovalField   `json:"tool"`
	Operation        ApprovalField   `json:"operation"`
	Targets          []ApprovalField `json:"targets"`
	Effect           ApprovalField   `json:"effect"`
	Scope            ApprovalField   `json:"scope"`
	Grants           []ApprovalGrant `json:"grants,omitempty"`
	TargetsTruncated bool            `json:"targets_truncated,omitempty"`
	// SensitiveInput signals that marked values were omitted from the display
	// request; it contains no secret values.
	SensitiveInput bool `json:"sensitive_input,omitempty"`
	// OpaqueInput marks private uninterpreted evidence, so derived review text
	// cannot accidentally expose its raw structure in client presentation.
	OpaqueInput     bool `json:"opaque_input,omitempty"`
	GrantsTruncated bool `json:"grants_truncated,omitempty"`
	// Reported marks remote display claims, independently of authoritative
	// action evidence. Clients must not treat reported scope as a grant.
	Reported bool `json:"reported,omitempty"`
}

const (
	approvalFieldLimit  = 512
	approvalTargetLimit = 16
	approvalGrantLimit  = 32
)

// KnownApprovalField bounds a field explicitly supplied by its semantic owner.
func KnownApprovalField(text string) ApprovalField {
	return normalizeApprovalField(ApprovalField{State: ApprovalFieldKnown, Text: text})
}

func normalizeApprovalField(in ApprovalField) ApprovalField {
	if in.Sensitive {
		return ApprovalField{State: ApprovalFieldOpaque, Sensitive: true}
	}
	switch in.State {
	case ApprovalFieldKnown, ApprovalFieldTruncated:
		in.Text = strings.TrimSpace(in.Text)
		if in.Text == "" {
			return ApprovalField{State: ApprovalFieldUnavailable}
		}
		runes := []rune(in.Text)
		if len(runes) > approvalFieldLimit {
			in.Text = string(runes[:approvalFieldLimit-3]) + "..."
			in.State = ApprovalFieldTruncated
		}
		return in
	case ApprovalFieldOpaque:
		return ApprovalField{State: ApprovalFieldOpaque}
	default:
		return ApprovalField{State: ApprovalFieldUnavailable}
	}
}

// CloneApprovalSubject detaches and bounds all display fields, including values
// supplied by embedding policies. Marked sensitive text is never copied.
func CloneApprovalSubject(in *ApprovalSubject) *ApprovalSubject {
	if in == nil {
		return nil
	}
	out := *in
	if in.Title != nil {
		field := normalizeApprovalField(*in.Title)
		out.Title = &field
	}
	if in.Description != nil {
		field := normalizeApprovalField(*in.Description)
		out.Description = &field
	}
	out.SensitiveInput = in.SensitiveInput || in.Tool.Sensitive || in.Operation.Sensitive || in.Effect.Sensitive || in.Scope.Sensitive
	out.SensitiveInput = out.SensitiveInput || (in.Title != nil && in.Title.Sensitive) || (in.Description != nil && in.Description.Sensitive)
	for _, field := range in.Targets {
		out.SensitiveInput = out.SensitiveInput || field.Sensitive
	}
	for _, grant := range in.Grants {
		out.SensitiveInput = out.SensitiveInput || grant.Scope.Sensitive
	}
	switch out.Kind {
	case "command", "file", "network", "app_access":
	default:
		out.Kind = "opaque"
	}
	out.Tool = normalizeApprovalField(in.Tool)
	out.Operation = normalizeApprovalField(in.Operation)
	out.Effect = normalizeApprovalField(in.Effect)
	out.Scope = normalizeApprovalField(in.Scope)
	out.Targets = make([]ApprovalField, 0, min(len(in.Targets), approvalTargetLimit))
	for _, field := range in.Targets[:min(len(in.Targets), approvalTargetLimit)] {
		out.Targets = append(out.Targets, normalizeApprovalField(field))
	}
	if len(out.Targets) == 0 {
		out.Targets = []ApprovalField{{State: ApprovalFieldUnavailable}}
	}
	out.TargetsTruncated = in.TargetsTruncated || len(in.Targets) > approvalTargetLimit
	out.Grants = make([]ApprovalGrant, 0, min(len(in.Grants), approvalGrantLimit))
	for _, grant := range in.Grants[:min(len(in.Grants), approvalGrantLimit)] {
		out.Grants = append(out.Grants, ApprovalGrant{OptionID: grant.OptionID, Scope: normalizeApprovalField(grant.Scope)})
	}
	out.GrantsTruncated = in.GrantsTruncated || len(in.Grants) > approvalGrantLimit
	if out.Reported && (out.SensitiveInput || out.OpaqueInput) {
		// Remote free text may repeat a marked value in an otherwise unmarked
		// field. Withdraw derived text as a whole rather than guessing which
		// substrings are secret. Keep presence and native choice identities.
		opaque := func(field ApprovalField) ApprovalField {
			if field.State == ApprovalFieldUnavailable {
				return field
			}
			return ApprovalField{State: ApprovalFieldOpaque, Sensitive: field.Sensitive}
		}
		if out.Title != nil {
			field := opaque(*out.Title)
			out.Title = &field
		}
		if out.Description != nil {
			field := opaque(*out.Description)
			out.Description = &field
		}
		out.Tool, out.Operation = opaque(out.Tool), opaque(out.Operation)
		out.Effect, out.Scope = opaque(out.Effect), opaque(out.Scope)
		for i := range out.Targets {
			out.Targets[i] = opaque(out.Targets[i])
		}
		for i := range out.Grants {
			out.Grants[i].Scope = opaque(out.Grants[i].Scope)
		}
	}
	return &out
}

// ApprovalSubjectOf returns a common live/replay projection. Legacy and remote
// approvals without owner facts receive an explicitly opaque subject. ACP kind,
// title, arbitrary raw input and option labels are never interpreted here.
func ApprovalSubjectOf(approval *ProtocolApproval) *ApprovalSubject {
	if approval != nil && approval.Subject != nil {
		out := CloneApprovalSubject(approval.Subject)
		out.Grants = nil
		// Join solely by the original native option identity and order. Missing
		// or duplicate scope facts stay unavailable, never inferred from labels.
		for _, option := range approval.Options {
			field := ApprovalField{State: ApprovalFieldUnavailable}
			matched := false
			for _, grant := range approval.Subject.Grants {
				if grant.OptionID != option.ID {
					continue
				}
				if matched {
					field = ApprovalField{State: ApprovalFieldUnavailable}
					break
				}
				field = normalizeApprovalField(grant.Scope)
				matched = true
			}
			out.Grants = append(out.Grants, ApprovalGrant{OptionID: option.ID, Scope: field})
		}
		return CloneApprovalSubject(out)
	}
	opaque := ApprovalField{State: ApprovalFieldOpaque}
	out := &ApprovalSubject{Kind: "opaque", Tool: opaque, Operation: opaque, Targets: []ApprovalField{opaque}, Effect: opaque, Scope: opaque}
	if approval != nil {
		if approval.ToolCall.NamePresent == nil || *approval.ToolCall.NamePresent {
			out.Tool = KnownApprovalField(approval.ToolCall.Name)
		}
		for _, option := range approval.Options {
			out.Grants = append(out.Grants, ApprovalGrant{OptionID: option.ID, Scope: opaque})
		}
	}
	return CloneApprovalSubject(out)
}

// ApprovalDisplaySubjectOf includes bounded remote display hints while keeping
// ApprovalSubjectOf and the exact permission evidence authoritative for policy.
// Original option identities alone determine the offered choices.
func ApprovalDisplaySubjectOf(approval *ProtocolApproval) *ApprovalSubject {
	canonical := ApprovalSubjectOf(approval)
	if approval == nil || approval.Context == nil || approval.Context.DisplaySubjectHint == nil {
		return canonical
	}
	hint := CloneApprovalSubject(approval.Context.DisplaySubjectHint)
	hint.Reported = true
	hint.SensitiveInput = hint.SensitiveInput || canonical.SensitiveInput
	hint.OpaqueInput = hint.OpaqueInput || canonical.OpaqueInput

	display := *approval
	display.Subject = hint
	return ApprovalSubjectOf(&display)
}
