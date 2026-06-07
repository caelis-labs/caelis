package acp

import (
	"encoding/json"
	"testing"
)

func TestLifecycleWireSchemaMatchesACP(t *testing.T) {
	resp := InitializeResponse{
		ProtocolVersion: 1,
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			MCPCapabilities: MCPCapabilities{
				HTTP: true,
				SSE:  true,
			},
			PromptCapabilities: PromptCapabilities{
				Image: true,
			},
		},
		AgentInfo: &Implementation{Name: "caelis", Title: "Caelis", Version: "test"},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion missing: %s", data)
	}
	caps, ok := got["agentCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("agentCapabilities missing: %s", data)
	}
	if _, ok := caps["mcpCapabilities"]; !ok {
		t.Fatalf("mcpCapabilities missing: %s", data)
	}
	if _, ok := caps["promptCapabilities"]; !ok {
		t.Fatalf("promptCapabilities missing: %s", data)
	}
}

func TestModeModelConfigWireFields(t *testing.T) {
	resp := NewSessionResponse{
		SessionID: "sess-1",
		ConfigOptions: []SessionConfigOption{{
			Type:         "select",
			ID:           "reasoning",
			Name:         "Reasoning",
			Category:     "model",
			CurrentValue: "medium",
			Options:      []SessionConfigSelectOption{{Value: "medium", Name: "Medium"}},
		}},
		Modes: &SessionModeState{
			AvailableModes: []SessionMode{{ID: "chat", Name: "Chat"}},
			CurrentModeID:  "chat",
		},
		Models: &SessionModelState{
			CurrentModelID:  "mimo",
			AvailableModels: []ModelInfo{{ModelID: "mimo", Name: "Mimo"}},
		},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	modes := got["modes"].(map[string]any)
	if modes["currentModeId"] != "chat" || modes["availableModes"] == nil {
		t.Fatalf("mode fields drifted: %s", data)
	}
	models := got["models"].(map[string]any)
	if models["currentModelId"] != "mimo" || models["availableModels"] == nil {
		t.Fatalf("model fields drifted: %s", data)
	}
	config := got["configOptions"].([]any)[0].(map[string]any)
	if config["currentValue"] != "medium" || config["options"] == nil || config["category"] != "model" {
		t.Fatalf("config fields drifted: %s", data)
	}
}

func TestPermissionWireShape(t *testing.T) {
	resp := PermissionSelectedOutcome(PermAllowOnce)
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Outcome.Outcome != "selected" || got.Outcome.OptionID != PermAllowOnce {
		t.Fatalf("permission response shape drifted: %s", data)
	}
	if PermAlways != "allow_always" || PermAlwaysReject != "reject_always" {
		t.Fatalf("permission constants drifted")
	}
}
