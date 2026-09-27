package modelconfig

import "testing"

func TestModelImageInputPreservesUnknownAndCatalogAuthority(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name string
		cfg  Config
		want *bool
	}{
		{"unknown", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture"}, nil},
		{"explicit-yes", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture", ImageInput: &yes}, &yes},
		{"explicit-no", Config{Provider: "openai-compatible", BaseURL: "https://private.invalid/v1", Model: "unlisted-fixture", ImageInput: &no}, &no},
		{"maintained-wins", Config{Provider: "openai", Model: "gpt-4.1", ImageInput: &no}, &yes},
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
				if tc.cfg.ImageInput != nil && *tc.cfg.ImageInput != *tc.want && tc.name != "maintained-wins" {
					t.Fatal("returned alias into model configuration")
				}
			}
		})
	}
}
