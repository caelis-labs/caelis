package session

import "testing"

func TestStableInvocationIdentity(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		provider     string
		model        string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "legacy xai response label",
			provider:     " XAI ",
			model:        " grok-4.5-build ",
			wantProvider: "xai",
			wantModel:    "grok-4.5",
		},
		{
			name:         "requested xai model",
			provider:     "xai",
			model:        "grok-4.5",
			wantProvider: "xai",
			wantModel:    "grok-4.5",
		},
		{
			name:         "xai sibling model",
			provider:     "xai",
			model:        "grok-4.20-build",
			wantProvider: "xai",
			wantModel:    "grok-4.20-build",
		},
		{
			name:         "other provider",
			provider:     "custom",
			model:        "grok-4.5-build",
			wantProvider: "custom",
			wantModel:    "grok-4.5-build",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, modelName := StableInvocationIdentity(test.provider, test.model)
			if provider != test.wantProvider || modelName != test.wantModel {
				t.Fatalf(
					"StableInvocationIdentity(%q, %q) = %q/%q, want %q/%q",
					test.provider,
					test.model,
					provider,
					modelName,
					test.wantProvider,
					test.wantModel,
				)
			}
		})
	}
}
