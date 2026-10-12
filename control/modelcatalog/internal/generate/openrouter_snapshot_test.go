package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOpenRouterRefreshPreservesHistoricalAndOtherProviders(t *testing.T) {
	existing := []byte(`{
		"openai:gpt-fixture":{"context_window":272000,"images":true,"future_field":{"value":7}},
		"openrouter:vendor/old":{"context_window":64000,"images":true,"reasoning":true},
		"openrouter:vendor/current":{"context_window":12345,"max_output":678,"images":false,"future_field":42,"hidden_from_directory":true},
		"openrouter:vendor/batch:batch":{"context_window":64000}
	}`)
	source := []byte(`{"data":[
		{"id":"vendor/new","context_length":1050000,"top_provider":{"context_length":1048576,"max_completion_tokens":128000},"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"supported_parameters":["tools","structured_outputs"],"reasoning":{"mandatory":true,"supported_efforts":["max","high","none"],"default_effort":"high"}},
		{"id":"vendor/current","context_length":999999,"top_provider":{"max_completion_tokens":99999},"architecture":{"output_modalities":["text"]},"supported_parameters":["tools"],"reasoning":{"mandatory":true}},
		{"id":"vendor/batch:batch","context_length":64000,"architecture":{"output_modalities":["text"]},"supported_parameters":["tools"]},
		{"id":"vendor/image","context_length":64000,"architecture":{"output_modalities":["text","image"]},"supported_parameters":["tools"]},
		{"id":"vendor/audio","context_length":64000,"architecture":{"output_modalities":["audio"]},"supported_parameters":["tools"]},
		{"id":"vendor/no-tools","context_length":64000,"architecture":{"output_modalities":["text"]},"supported_parameters":[]}
	]}`)
	refreshed, err := refreshOpenRouterSnapshot(existing, source)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(existing, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(refreshed, &after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before["openai:gpt-fixture"], after["openai:gpt-fixture"]) || len(after) != 5 {
		t.Fatalf("unrelated data changed or unsupported routes added: %s", refreshed)
	}
	var old, current, added capEntry
	for key, target := range map[string]*capEntry{
		"openrouter:vendor/old": &old, "openrouter:vendor/current": &current, "openrouter:vendor/new": &added,
	} {
		if err := json.Unmarshal(after[key], target); err != nil {
			t.Fatal(err)
		}
	}
	if !old.HiddenFromDirectory || old.ContextWindow != 64000 || !old.Images || !old.Reasoning {
		t.Fatalf("historical capability lookup changed: %#v", old)
	}
	if current.HiddenFromDirectory || current.ContextWindow != 999999 || current.MaxOutput != 99999 || current.Images ||
		!current.Reasoning || current.ReasoningMode != "none" || len(current.ReasoningEfforts) != 0 {
		t.Fatalf("current record compatibility or omitted controls changed: %#v", current)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(after["openrouter:vendor/current"], &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["future_field"]) != "42" {
		t.Fatalf("unknown historical field lost: %s", after["openrouter:vendor/current"])
	}
	if added.ContextWindow != 1048576 || added.MaxOutput != 128000 || !added.ToolCalls || !added.Images || !added.JSONOutput ||
		added.ReasoningMode != "effort" || !reflect.DeepEqual(added.ReasoningEfforts, []string{"max", "high"}) || added.DefaultReasoningEffort != "high" {
		t.Fatalf("official route metadata = %#v", added)
	}
	if _, exists := after["vendor:new"]; exists {
		t.Fatal("routed capability leaked into native provider")
	}
	again, err := refreshOpenRouterSnapshot(refreshed, source)
	if err != nil || !bytes.Equal(refreshed, again) {
		t.Fatalf("refresh is not deterministic/idempotent: %v", err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.gz")
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(existing); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "official.json")
	if err := os.WriteFile(input, source, 0o600); err != nil {
		t.Fatal(err)
	}
	generated, err := generateSnapshot("", "", input, path)
	if err != nil || !bytes.Equal(generated, refreshed) {
		t.Fatalf("existing gzip generation path = %s, %v", generated, err)
	}
}

func TestOpenRouterReasoningNullAndOmittedAreDifferent(t *testing.T) {
	for _, tc := range []struct {
		reasoning string
		mode      string
		efforts   []string
	}{
		{reasoning: `{"mandatory":true}`, mode: "none"},
		{reasoning: `{"mandatory":false,"supported_efforts":null}`, mode: "effort", efforts: []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}},
		{reasoning: `{"mandatory":true,"supported_efforts":null}`, mode: "effort", efforts: []string{"max", "xhigh", "high", "medium", "low", "minimal"}},
		{reasoning: `{"supported_efforts":["high","none"],"default_effort":"high"}`, mode: "effort", efforts: []string{"high", "none"}},
	} {
		var model openRouterModel
		if err := json.Unmarshal([]byte(`{"reasoning":`+tc.reasoning+`}`), &model); err != nil {
			t.Fatal(err)
		}
		entry, err := openRouterCapabilities(model)
		if err != nil || entry.ReasoningMode != tc.mode || !reflect.DeepEqual(entry.ReasoningEfforts, tc.efforts) {
			t.Fatalf("%s: %#v, %v", tc.reasoning, entry, err)
		}
	}
}

func TestOpenRouterRefreshRejectsEmptyOrMalformedSources(t *testing.T) {
	for _, source := range []string{`{}`, `{"data":[]}`, `{"data":[{"id":"no-tools"}]}`, `invalid`,
		`{"total_count":2,"data":[{"id":"vendor/current","context_length":64000,"architecture":{"output_modalities":["text"]},"supported_parameters":["tools"]}]}`,
		`{"links":{"next":"https://openrouter.ai/api/v1/models?cursor=next"},"data":[{"id":"vendor/current","context_length":64000,"architecture":{"output_modalities":["text"]},"supported_parameters":["tools"]}]}`,
	} {
		if _, err := refreshOpenRouterSnapshot([]byte(`{"openrouter:vendor/old":{"context_window":64000}}`), []byte(source)); err == nil {
			t.Fatalf("accepted unusable source %q", source)
		}
	}
}
