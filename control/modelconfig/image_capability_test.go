package modelconfig

import "testing"

func TestModelImageInputPreservesUnknownAndAcceptedSnapshot(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		cfg  Config
		want *bool
	}{
		{"unknown", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture"}, nil},
		{"explicit-yes", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture", ImageInput: &yes}, &yes},
		{"explicit-no", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture", ImageInput: &no}, &no},
		{"saved-wins", Config{Provider: "openai-codex", Model: "gpt-6.1-sol", ImageInput: &no}, &no},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelImageInput(tc.cfg)
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("capability=%v, want=%v", got, tc.want)
			}
			if ModelSupportsImages(tc.cfg) != (got != nil && *got) {
				t.Fatal("metadata and execution diverged")
			}
			if got != nil {
				*got = !*got
				if tc.cfg.ImageInput != nil && *tc.cfg.ImageInput != *tc.want {
					t.Fatal("returned alias into model configuration")
				}
			}
		})
	}
}
