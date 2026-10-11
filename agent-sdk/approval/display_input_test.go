package approval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestApprovalDisplayRedactsOnlySchemaMarkedInput(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"nested":{"type":"array","items":{"$ref":"#/$defs/secret"}},"justification":{"writeOnly":true}},"$defs":{"secret":{"type":"object","properties":{"credential":{"writeOnly":true},"target":{"type":"string"}}}}}`), &schema); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"nested": []any{map[string]any{"credential": "MARKED-CREDENTIAL", "target": "/docs"}}, "unknown": map[string]any{"token": "UNMARKED-IS-NOT-SANITIZED"}, "justification": "MARKED-JUSTIFICATION"}
	options := []session.ProtocolApprovalOption{{ID: "native-session", Name: "Native session choice", Kind: "allow_once"}, {ID: "native-reject", Name: "Reject", Kind: "reject_once"}}
	callInput, _ := json.Marshal(input)
	req := agentsdk.ApprovalRequest{Tool: tool.Definition{Name: "custom", InputSchema: schema}, Call: tool.Call{ID: "call", Input: callInput},
		Approval: &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call", Name: "custom", Title: "MARKED-CREDENTIAL", RawInput: input, RawOutput: map[string]any{"echo": "MARKED-CREDENTIAL"}}, Options: options}, Metadata: map[string]any{"justification": "MARKED-JUSTIFICATION"}}
	canonical := PayloadFromRuntimeRequest(req)
	if canonical.RawInput["justification"] != "MARKED-JUSTIFICATION" || canonical.Justification != "MARKED-JUSTIFICATION" || canonical.ToolTitle != "MARKED-CREDENTIAL" {
		t.Fatalf("display redaction polluted reviewer evidence: %+v", canonical)
	}
	payload := DisplayPayloadFromRuntimeRequest(req)
	protocol := ProtocolApprovalFromPayload(payload)
	raw, _ := json.Marshal(protocol)
	for _, secret := range []string{"MARKED-CREDENTIAL", "MARKED-JUSTIFICATION"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("marked value in presentation: %s", raw)
		}
	}
	if !reflect.DeepEqual(protocol.Options, options) || string(req.Call.Input) != string(callInput) || req.Approval.ToolCall.Title != "MARKED-CREDENTIAL" {
		t.Fatalf("redaction changed execution evidence: %s", raw)
	}
	safeInput, _ := RedactInputForDisplay(input, schema)
	safeRaw, _ := json.Marshal(safeInput)
	if !strings.Contains(string(safeRaw), "UNMARKED-IS-NOT-SANITIZED") || !strings.Contains(string(safeRaw), "/docs") {
		t.Fatalf("marked-only redactor claimed unmarked safety: %s", safeRaw)
	}
	if payload.Subject.Kind != "opaque" {
		t.Fatalf("nested unknown input was interpreted: %+v", payload.Subject)
	}
}

func TestApprovalDisplayRedactsPatternAndAdditionalSchemaMarkers(t *testing.T) {
	for _, rawSchema := range []string{
		`{"properties":{"target":{"type":"string"}},"patternProperties":{"^credential_":{"writeOnly":true}}}`,
		`{"properties":{"target":{"type":"string"}},"additionalProperties":{"writeOnly":true}}`,
		`{"properties":{"target":{"type":"string"}},"unevaluatedProperties":{"writeOnly":true}}`,
		`{"allOf":[{"patternProperties":{"^credential_":{"writeOnly":true}}}],"properties":{"target":{"type":"string"}}}`,
	} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(rawSchema), &schema); err != nil {
			t.Fatal(err)
		}
		copy, marked := RedactInputForDisplay(map[string]any{"target": "https://example.test", "credential_header": "MARKED-PATTERN-SECRET"}, schema)
		raw, _ := json.Marshal(copy)
		if !marked || strings.Contains(string(raw), "MARKED-PATTERN-SECRET") || !strings.Contains(string(raw), "https://example.test") {
			t.Fatalf("schema %s leaked or removed target: %s", rawSchema, raw)
		}
	}
}

func TestApprovalDisplayBoundsRecursiveCombinators(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{"$ref":"#/$defs/cycle","$defs":{"cycle":{"allOf":[{"$ref":"#/$defs/cycle"},{"$ref":"#/$defs/cycle"}],"properties":{"credential":{"writeOnly":true}}}}}`), &schema); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	copy, marked := RedactInputForDisplay(map[string]any{"credential": "RECURSIVE-MARKED-SECRET"}, schema)
	raw, _ := json.Marshal(copy)
	if !marked || strings.Contains(string(raw), "RECURSIVE-MARKED-SECRET") || time.Since(started) > time.Second {
		t.Fatalf("recursive schema did not fail closed within work bounds: %s", raw)
	}
}

func TestApprovalDisplayBoundsWidePatternEvaluation(t *testing.T) {
	input, patterns := map[string]any{}, map[string]any{}
	for i := range 300 {
		input[fmt.Sprintf("value_%d", i)] = "WIDE-MARKED-SECRET"
		patterns[fmt.Sprintf("^never_%d$", i)] = map[string]any{"writeOnly": true}
	}
	started := time.Now()
	copy, opaque := RedactInputForDisplay(input, map[string]any{"patternProperties": patterns})
	raw, _ := json.Marshal(copy)
	if !opaque || strings.Contains(string(raw), "WIDE-MARKED-SECRET") || time.Since(started) > time.Second {
		t.Fatalf("wide schema did not fail closed within work bounds: %s", raw)
	}
}

func TestApprovalDisplayHonorsOwnerSensitiveFactWithoutSchema(t *testing.T) {
	req := agentsdk.ApprovalRequest{Tool: tool.Definition{Name: "custom"}, Call: tool.Call{Input: json.RawMessage(`{"opaque":"OWNER-MARKED-SECRET"}`)},
		Approval: &session.ProtocolApproval{Subject: &session.ApprovalSubject{Kind: "opaque", Targets: []session.ApprovalField{{State: session.ApprovalFieldKnown, Text: "OWNER-MARKED-SECRET", Sensitive: true}}}, ToolCall: session.ProtocolToolCall{Name: "custom", Title: "OWNER-MARKED-SECRET"}}}
	canonical := PayloadFromRuntimeRequest(req)
	display := DisplayPayloadFromRuntimeRequest(req)
	raw, _ := json.Marshal(display)
	if canonical.RawInput["opaque"] != "OWNER-MARKED-SECRET" || !display.Subject.SensitiveInput || strings.Contains(string(raw), "OWNER-MARKED-SECRET") || DisplayReviewText("OWNER-MARKED-SECRET", display.Subject) != "Sensitive action details withheld." {
		t.Fatalf("owner marking lost or evidence changed: %s", raw)
	}
}
