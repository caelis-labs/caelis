package client

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSupportsSessionSteeringValidatesCapabilityWithoutMutatingMeta(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		meta      map[string]json.RawMessage
		supported bool
		wantErr   bool
	}{
		{name: "missing"},
		{name: "empty object", meta: steeringCapabilityMeta(`{}`)},
		{name: "supported", meta: steeringCapabilityMeta(`{"supported":true}`), supported: true},
		{name: "unsupported", meta: steeringCapabilityMeta(`{"supported":false}`)},
		{name: "unknown sibling", meta: map[string]json.RawMessage{
			SessionSteeringMetaKey: json.RawMessage(`{"supported":true,"future":{"sequence":9007199254740993}}`),
			"vendor.example":       json.RawMessage(`{"raw":9007199254740993}`),
		}, supported: true},
		{name: "outer null", meta: steeringCapabilityMeta(`null`), wantErr: true},
		{name: "outer array", meta: steeringCapabilityMeta(`[]`), wantErr: true},
		{name: "outer string", meta: steeringCapabilityMeta(`"yes"`), wantErr: true},
		{name: "outer invalid JSON", meta: steeringCapabilityMeta(`{`), wantErr: true},
		{name: "supported null", meta: steeringCapabilityMeta(`{"supported":null}`), wantErr: true},
		{name: "supported string", meta: steeringCapabilityMeta(`{"supported":"true"}`), wantErr: true},
		{name: "supported number", meta: steeringCapabilityMeta(`{"supported":1}`), wantErr: true},
		{name: "supported object", meta: steeringCapabilityMeta(`{"supported":{}}`), wantErr: true},
		{name: "supported array", meta: steeringCapabilityMeta(`{"supported":[]}`), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before := cloneSteeringCapabilityMeta(tt.meta)
			got, err := SupportsSessionSteering(InitializeResponse{Meta: tt.meta})
			if (err != nil) != tt.wantErr {
				t.Fatalf("SupportsSessionSteering() = %v, %v; want error=%v", got, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.supported {
				t.Fatalf("SupportsSessionSteering() = %v, want %v", got, tt.supported)
			}
			if len(tt.meta) != len(before) {
				t.Fatalf("initialize _meta length changed: got %d want %d", len(tt.meta), len(before))
			}
			for key, want := range before {
				if !bytes.Equal(tt.meta[key], want) {
					t.Fatalf("initialize _meta[%q] = %q, want original %q", key, tt.meta[key], want)
				}
			}
		})
	}
}

func steeringCapabilityMeta(raw string) map[string]json.RawMessage {
	return map[string]json.RawMessage{SessionSteeringMetaKey: json.RawMessage(raw)}
}

func cloneSteeringCapabilityMeta(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
