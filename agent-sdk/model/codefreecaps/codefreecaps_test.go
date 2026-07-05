package codefreecaps

import "testing"

func TestLookupKnownModels(t *testing.T) {
	tests := []struct {
		model     string
		context   int
		maxOutput int
		image     bool
		known     bool
	}{
		{model: "DeepSeek-V4-Flash-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "deepseek-v4-flash-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-4.7", context: 80000, maxOutput: 8000, image: false, known: true},
		{model: "GLM-5-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-5.1", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "GLM-5.1-ctyun-oc", context: 112000, maxOutput: 16000, image: false, known: true},
		{model: "Qwen3.5-122B-A10B", context: 112000, maxOutput: 16000, image: true, known: true},
		{model: "custom-codefree-model", context: 128000, maxOutput: 8000, image: false, known: false},
	}
	for _, tt := range tests {
		got, ok := Lookup(tt.model)
		if ok != tt.known {
			t.Fatalf("Lookup(%q) ok = %v, want %v", tt.model, ok, tt.known)
		}
		contextWindow := UnknownContextWindowTokens
		maxOutput := UnknownMaxOutputTokens
		image := false
		if ok {
			contextWindow = got.ContextWindowTokens
			maxOutput = got.MaxOutputTokens
			image = got.SupportsImages
		}
		if contextWindow != tt.context || maxOutput != tt.maxOutput || image != tt.image {
			t.Fatalf("Lookup(%q) = limits %d/%d image %v, want %d/%d image %v",
				tt.model, contextWindow, maxOutput, image, tt.context, tt.maxOutput, tt.image)
		}
	}
}

func TestLookupPrefixMatchingUsesLongestID(t *testing.T) {
	got, ok := Lookup("glm-5.1-ctyun-oc-suffix")
	if !ok {
		t.Fatal("Lookup(prefix) ok = false, want true")
	}
	if got.ID != "GLM-5.1-ctyun-oc" {
		t.Fatalf("Lookup(prefix) ID = %q, want GLM-5.1-ctyun-oc", got.ID)
	}

	got, ok = Lookup("glm-5.1-preview")
	if !ok {
		t.Fatal("Lookup(prefix) ok = false, want true")
	}
	if got.ID != "GLM-5.1" {
		t.Fatalf("Lookup(prefix) ID = %q, want GLM-5.1", got.ID)
	}
}

func TestLookupUnknownFallbackConstants(t *testing.T) {
	if UnknownContextWindowTokens != 128000 {
		t.Fatalf("UnknownContextWindowTokens = %d, want 128000", UnknownContextWindowTokens)
	}
	if UnknownMaxOutputTokens != 8000 {
		t.Fatalf("UnknownMaxOutputTokens = %d, want 8000", UnknownMaxOutputTokens)
	}
	_, ok := Lookup("totally-unknown-codefree-model")
	if ok {
		t.Fatal("Lookup(unknown) ok = true, want false")
	}
}

func TestSupportsImageInputs(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "Qwen3.5-122B-A10B", want: true},
		{model: "qwen3.5-122b-a10b", want: true},
		{model: "GLM-4.7", want: false},
		{model: "custom-codefree-model", want: false},
		{model: "", want: false},
	}
	for _, tt := range tests {
		if got := SupportsImageInputs(tt.model); got != tt.want {
			t.Fatalf("SupportsImageInputs(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}
