package model

import (
	"reflect"
	"testing"
)

func TestCloneRequestIsolatesTypedJSONBindings(t *testing.T) {
	schema := map[string]any{"required": []string{"real"}, "nested": map[string][]map[string][]string{"groups": {{"values": {"original"}}}}, "array": [1]map[string][]string{{"names": {"original"}}}}
	original := &Request{Tools: []ToolSpec{{Function: &FunctionToolSpec{Name: "Read", Parameters: schema}}}, Output: &OutputSpec{JSONSchema: schema}}
	expected := CloneRequest(original)
	clone := CloneRequest(original)
	for _, s := range []map[string]any{clone.Tools[0].Function.Parameters, clone.Output.JSONSchema} {
		s["required"].([]string)[0] = "invented"
		s["nested"].(map[string][]map[string][]string)["groups"][0]["values"][0] = "invented"
		s["array"].([1]map[string][]string)[0]["names"][0] = "invented"
	}
	if !reflect.DeepEqual(original, expected) {
		t.Fatalf("typed schema leaked through clone: %#v", original)
	}
}
