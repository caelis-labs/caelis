package client

import (
	"encoding/json"
	"testing"
)

func TestLegacyModelChannelRemainsHostPrivateWireFallback(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"sessionId":"legacy-session",
		"models":{
			"currentModelId":"sonnet/high",
			"availableModels":[
				{"modelId":"sonnet/high","name":"Sonnet High"},
				{"modelId":"opus/high","name":"Opus High","description":"legacy fallback"}
			]
		}
	}`)
	var response NewSessionResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.SessionID != "legacy-session" || response.Models == nil || response.Models.CurrentModelID != "sonnet/high" || len(response.Models.AvailableModels) != 2 {
		t.Fatalf("legacy new-session response = %#v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["models"]; !ok {
		t.Fatalf("encoded response = %s, want legacy models fallback", encoded)
	}

	request, err := json.Marshal(SetSessionModelRequest{SessionID: "legacy-session", ModelID: "opus/high"})
	if err != nil {
		t.Fatal(err)
	}
	if string(request) != `{"sessionId":"legacy-session","modelId":"opus/high"}` {
		t.Fatalf("set-model request = %s", request)
	}
}
