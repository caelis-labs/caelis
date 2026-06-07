package model

// Ref identifies a model. Either ModelID or Alias must be set.
type Ref struct {
	ModelID string
	Alias   string
}

// ModelInfo describes a model's capabilities.
type ModelInfo struct {
	ModelID       string
	DisplayName   string
	Provider      string
	MaxTokens     int
	SupportsTools bool
	SupportsImage bool
	SupportsAudio bool
	Aliases       []string
}
