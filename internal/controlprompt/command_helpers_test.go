package controlprompt

import "testing"

func TestParseConnectArgsKeepsLegacyStreamTimeoutAndOptionalImageInput(t *testing.T) {
	cfg := ParseConnectArgs("openai-compatible acme-vision https://models.acme.example/v1 120 - 131072 8192 low,high 45 true")

	if cfg.Provider != "openai-compatible" || cfg.Model != "acme-vision" {
		t.Fatalf("parsed identity = %q/%q", cfg.Provider, cfg.Model)
	}
	if cfg.StreamFirstEventTimeoutSeconds != 45 {
		t.Fatalf("stream timeout = %d, want 45", cfg.StreamFirstEventTimeoutSeconds)
	}
	if cfg.ImageInput == nil || !*cfg.ImageInput {
		t.Fatalf("image input = %v, want explicit true", cfg.ImageInput)
	}

	legacy := ParseConnectArgs("openai-compatible acme-text https://models.acme.example/v1 120 - 131072 8192 low,high 45")
	if legacy.StreamFirstEventTimeoutSeconds != 45 || legacy.ImageInput != nil {
		t.Fatalf("legacy parse = %#v, want stream timeout without image override", legacy)
	}
}
