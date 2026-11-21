package sandboxmeta

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestConstraintsDecodesCallMetadata(t *testing.T) {
	rules := []sandbox.PathRule{{Path: " scratch ", Access: " read_write "}}
	normalized := sandbox.Constraints{
		Route: sandbox.RouteSandbox, Network: sandbox.NetworkDisabled,
		PathRules: []sandbox.PathRule{{Path: "scratch", Access: sandbox.PathAccessReadWrite}},
	}
	for _, tc := range []struct {
		name string
		meta map[string]any
		want sandbox.Constraints
		ok   bool
		bad  bool
	}{
		{name: "missing"},
		{name: "nil", meta: map[string]any{ConstraintsKey: nil}},
		{name: "invalid encoding", bad: true, meta: map[string]any{ConstraintsKey: func() {}}},
		{name: "invalid shape", bad: true, meta: map[string]any{ConstraintsKey: map[string]any{"route": "host", "path_rules": true}}},
		{name: "empty typed", meta: map[string]any{ConstraintsKey: sandbox.Constraints{}}, ok: true},
		{name: "empty object", meta: map[string]any{ConstraintsKey: map[string]any{}}, ok: true},
		{name: "encoded null", meta: map[string]any{ConstraintsKey: json.RawMessage("null")}, ok: true},
		{name: "typed", meta: map[string]any{ConstraintsKey: sandbox.Constraints{Route: " sandbox ", Network: " disabled ", PathRules: rules}}, want: normalized, ok: true},
		{name: "JSON-compatible", meta: map[string]any{ConstraintsKey: map[string]any{
			"route": " sandbox ", "network": " disabled ", "path_rules": []any{map[string]any{"path": " scratch ", "access": " read_write "}},
		}}, want: normalized, ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := DecodeConstraints(tc.meta)
			if ok != tc.ok || (err != nil) != tc.bad || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DecodeConstraints() = %+v, %t, %v, want %+v, %t, error=%t", got, ok, err, tc.want, tc.ok, tc.bad)
			}
			if len(got.PathRules) != 0 {
				got.PathRules[0].Path = "edited"
			}
		})
	}
	if rules[0].Path != " scratch " || rules[0].Access != " read_write " {
		t.Fatalf("decoder modified the caller's path rules: %+v", rules)
	}
}
