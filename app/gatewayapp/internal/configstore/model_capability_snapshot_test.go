package configstore

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
)

func TestModelCapabilityUpgradeDoesNotMutateCallerSnapshot(t *testing.T) {
	models := PersistedModelConfig{Configs: []modelconfig.Config{{Provider: "openai", Model: "gpt-6-sol"}}}
	before, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	upgraded := normalizePersistedModelsForSave(models)
	after, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("normalization changed the caller's uncommitted snapshot")
	}
	if upgraded.Configs[0].ImageInput == nil || !*upgraded.Configs[0].ImageInput || upgraded.Configs[0].ContextWindowTokens <= 0 {
		t.Fatal("upgrade did not reconstruct omitted capabilities")
	}
}
