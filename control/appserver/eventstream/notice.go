package eventstream

// IsSessionNotice identifies transient Session observations with no Turn,
// participant, approval, or tool ownership. They remain meaningful while idle.
func IsSessionNotice(envelope Envelope) bool {
	return envelope.Kind == KindNotice && envelope.SessionID != "" &&
		envelope.HandleID == "" && envelope.RunID == "" && envelope.TurnID == "" && envelope.ActivityID == "" &&
		(envelope.Scope == "" || envelope.Scope == ScopeMain) && envelope.ScopeID == "" &&
		envelope.ParticipantID == "" && envelope.ParentTool == nil && envelope.ApprovalRequestID == "" &&
		envelope.Delivery != nil && envelope.Delivery.Mode == DeliveryTransient
}
