package application

// ModelCapabilities describes the desired application model for subsequent
// model requests. It is an observation, not a dispatch grant or a description of
// an already-issued request. ConfigurationRevision belongs to the application
// profile; it does not version the provider catalog or canonical Session history.
// ImageInput is nil when the selected model has no authoritative declaration.
type ModelCapabilities struct {
	SessionID             string `json:"session_id"`
	ConfigurationRevision uint64 `json:"configuration_revision"`
	Model                 string `json:"model"`
	ImageInput            *bool  `json:"image_input,omitempty"`
}
