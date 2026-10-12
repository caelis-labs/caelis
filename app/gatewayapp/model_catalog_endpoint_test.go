package gatewayapp

import (
	"github.com/caelis-labs/caelis/control/modelconfig"
	"strings"
	"testing"
)

func TestOllamaCloudCapabilitiesUseEndpointCatalog(t *testing.T) {
	t.Parallel()

	glm := modelconfig.RestoreSavedCapabilities(ModelConfig{
		Provider: "ollama",
		Model:    "glm-5.2",
		BaseURL:  "https://ollama.com",
	})
	if got := strings.Join(reasoningLevelsForACPModel(glm), ","); got != "high,max" {
		t.Fatalf("reasoningLevelsForACPModel(Ollama Cloud GLM) = %q, want high,max", got)
	}

	kimi := modelconfig.RestoreSavedCapabilities(ModelConfig{
		Provider: "ollama",
		Model:    "kimi-k3",
		BaseURL:  "https://ollama.com",
	})
	if !modelConfigSupportsImages(kimi) {
		t.Fatal("modelConfigSupportsImages(Ollama Cloud Kimi) = false, want true")
	}
}
