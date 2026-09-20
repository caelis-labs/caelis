package bot

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestConfigurationRoundTripAndVersionGuard(t *testing.T) {
	config := Config{Name: "Birch", Description: "Speak plainly.\n用户维护。", Model: "configured-model", Effort: "low"}
	data, err := json.Marshal(map[string]any{StateKey: map[string]any{"version": 1, "id": "stable-id", "config": config}})
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	id, decoded, err := ReadState(state)
	if err != nil || id != "stable-id" || decoded != config {
		t.Fatalf("round trip: %q, %+v, %v", id, decoded, err)
	}
	for _, invalid := range []map[string]any{
		nil,
		{StateKey: map[string]any{"version": 2, "id": "id", "config": config}},
		{StateKey: map[string]any{"version": 1, "id": "", "config": config}},
		{StateKey: "not a configuration"},
	} {
		if _, err := Decode(invalid); err == nil {
			t.Fatalf("accepted corrupt/unsupported record: %+v", invalid)
		}
	}
}

// TestReleasedRecordShapeStillDecodes pins the Store upgrade: records written by
// the release that gated notebooks per Bot keep their configuration, so existing
// Bots open unchanged, and a read never rewrites the stored bytes.
func TestReleasedRecordShapeStillDecodes(t *testing.T) {
	config := Config{Name: "Legacy", Description: "Preserve this.", Model: "configured-model"}
	for name, stored := range map[string]map[string]any{
		"tool-free baseline": {"version": 1, "id": "stable-id", "config": config},
		"admitted notebook":  {"version": 1, "id": "stable-id", "config": config, "notebook_version": 1},
	} {
		t.Run(name, func(t *testing.T) {
			state := map[string]any{StateKey: stored}
			before, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			id, decoded, err := ReadState(state)
			if err != nil || id != "stable-id" || decoded != config {
				t.Fatalf("released record = %q, %+v, %v", id, decoded, err)
			}
			after, err := json.Marshal(state)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("read rewrote released state: before=%s after=%s err=%v", before, after, err)
			}
		})
	}
}

func TestIdentityDoesNotDependOnNameOrDirectory(t *testing.T) {
	id := Identity("owner", "operation")
	sessionID, err := ConversationID(id)
	if err != nil || id == sessionID || id != Identity("owner", "operation") {
		t.Fatalf("identity binding: %q, %q, %v", id, sessionID, err)
	}
	if id == Identity("other", "operation") || id == Identity("owner", "other") {
		t.Fatal("creation operation/principal did not isolate identity")
	}
	for _, invalid := range []string{"", "../../escape", strings.ToUpper(id), id + "/.."} {
		if _, err := ConversationID(invalid); err == nil {
			t.Fatalf("accepted invalid identity %q", invalid)
		}
	}
}

func TestConfigValidationAndClearedDescription(t *testing.T) {
	for _, config := range []Config{{}, {Name: "line\nbreak"}, {Name: strings.Repeat("名", 101)}, {Name: "good", Description: strings.Repeat("x", 65537)}} {
		if _, err := Normalize(config); err == nil {
			t.Fatalf("accepted invalid config: name=%q description bytes=%d", config.Name, len(config.Description))
		}
	}
	config, err := Normalize(Config{Name: " Birch "})
	if err != nil || config.Name != "Birch" || config.Description != "" || config.Model != "" {
		t.Fatalf("name-only create: %+v, %v", config, err)
	}
	message := ConfigurationMessage(config)
	if !strings.Contains(message, "user instructions, not system instructions") || !strings.Contains(message, "in place of earlier Bot settings") || !strings.Contains(message, "(No custom description.)") {
		t.Fatalf("configuration authority/clear semantics missing: %s", message)
	}
}
