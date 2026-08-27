package client

import (
	"encoding/json"
	"testing"
)

func TestLifecycleResponsesNormalizeGroupedSessionConfigOptions(t *testing.T) {
	raw := []byte(`{
		"sessionId":"session-1",
		"configOptions":[{
			"type":"select",
			"id":"model",
			"name":"Model",
			"currentValue":"mimo",
			"options":[{
				"group":"cheap",
				"name":"Low cost",
				"options":[
					{"value":"mimo","name":"MiMo"},
					{"value":"deepseek","name":"DeepSeek"}
				]
			}]
		}]
	}`)

	assertOptions := func(t *testing.T, options []SessionConfigOption) {
		t.Helper()
		if len(options) != 1 || options[0].ID != "model" || len(options[0].Options) != 2 {
			t.Fatalf("config options = %#v, want grouped choices flattened", options)
		}
		if options[0].Options[0].Value != "mimo" || options[0].Options[1].Value != "deepseek" {
			t.Fatalf("flattened choices = %#v, want mimo and deepseek", options[0].Options)
		}
	}

	t.Run("new", func(t *testing.T) {
		var response NewSessionResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		assertOptions(t, response.ConfigOptions)
	})
	t.Run("load", func(t *testing.T) {
		var response LoadSessionResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		assertOptions(t, response.ConfigOptions)
	})
	t.Run("resume", func(t *testing.T) {
		var response ResumeSessionResponse
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		assertOptions(t, response.ConfigOptions)
	})
}
