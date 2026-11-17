package tool

// SourceFacts identifies the configured source of a callable. Only trusted
// assembly and the source owner may populate it; remote declarations cannot
// establish an integration identity. Revisions describe content, not refreshes.
type SourceFacts struct {
	IntegrationID      string `json:"integration_id,omitempty"`
	ID                 string `json:"id"`
	Revision           string `json:"revision"`
	CapabilityRevision string `json:"capability_revision,omitempty"`
}
