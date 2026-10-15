package wirev1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationContentFixturesConformToOpenAPI(t *testing.T) {
	for _, fixture := range []struct{ file, schema string }{
		{"application-create.json", "CreateSessionRequest"},
		{"application-guardian-create.json", "CreateSessionRequest"},
		{"application-reviewer-state.json", "SessionReviewerState"},
		{"application-tool-content-v1.json", "SessionToolDefinition"},
		{"application-result-legacy.json", "SessionToolCallResult"},
		{"application-result-legacy-text.json", "SessionToolCallResult"},
		{"application-result-content-v1.json", "SessionToolCallResult"},
		{"application-result-empty-structured.json", "SessionToolCallResult"},
		{"application-resource-expiring.json", "SessionResourceRequest"},
		{"application-resource-expiring-descriptor.json", "SessionResource"},
	} {
		t.Run(fixture.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../../api/control/v1/fixtures", fixture.file))
			if err != nil {
				t.Fatal(err)
			}
			if err := openAPIValidator(t, fixture.schema).Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("%s: %v", fixture.file, err)
			}
			if fixture.schema == "SessionResourceRequest" {
				var request generated.SessionResourceRequest
				if err := json.Unmarshal(raw, &request); err != nil || request.ExpiresAt == nil || !request.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("generated Go lost resource request expiration: %#v, %v", request, err)
				}
				return
			}
			if fixture.schema == "SessionResource" {
				var resource generated.SessionResource
				if err := json.Unmarshal(raw, &resource); err != nil || resource.ExpiresAt == nil || !resource.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("generated Go lost resource descriptor expiration: %#v, %v", resource, err)
				}
				return
			}
			if fixture.schema != "SessionToolCallResult" {
				return
			}
			// Generated Go must preserve both opaque legacy content and new typed
			// block content; the TypeScript generated DTO is a discriminated union.
			var result generated.SessionToolCallResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeJSONWithNumbers(t, raw), decodeJSONWithNumbers(t, roundTrip)) {
				t.Fatalf("generated Go lost callback fields: got %s, want %s", roundTrip, raw)
			}
		})
	}
}

func TestApplicationContentGeneratedEmptyObjectIsPresent(t *testing.T) {
	result := generated.SessionContentCallResult{Outcome: "succeeded", ResultFormat: "content-v1", Content: []generated.SessionResultContentBlock{}, StructuredContent: generated.JSONObject{}}
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), `"structuredContent":{}`) {
		t.Fatalf("generated client lost an empty structured object: %s %v", raw, err)
	}
}

func TestApplicationContentSchemaRejectsInvalidFormatsAndBlocks(t *testing.T) {
	cases := []struct{ name, schema, body string }{
		{"unknown-tool-format", "SessionToolDefinition", `{"name":"t","description":"t","input_schema":{},"result_format":"future"}`},
		{"legacy-output-schema", "SessionToolDefinition", `{"name":"t","description":"t","input_schema":{},"output_schema":{}}`},
		{"unknown-result-format", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"future","content":[]}`},
		{"legacy-structured-content", "SessionToolCallResult", `{"outcome":"succeeded","content":"ok","structuredContent":{}}`},
		{"not-block-array", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":"text"}`},
		{"unexpected-block", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"audio","data":"YQ=="}]}`},
		{"gif", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"image","data":"YQ==","mimeType":"image/gif"}]}`},
		{"file-uri", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"resource_link","uri":"file:///tmp/a.png","name":"a.png","mimeType":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]}`},
		{"too-many-blocks", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[` + strings.Repeat(`{"type":"text","text":"x"},`, 64) + `{"type":"text","text":"x"}]}`},
		{"text-block-over-model-budget", "SessionToolCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"text","text":"` + strings.Repeat("x", 32769) + `"}]}`},
		{"invalid-expiry", "SessionResourceRequest", `{"operation_id":"upload-1","name":"image.png","media_type":"image/png","data":"YQ==","sha256":"` + strings.Repeat("a", 64) + `","expires_at":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := openAPIValidator(t, tc.schema).Validate(decodeJSONWithNumbers(t, []byte(tc.body))); err == nil {
				t.Fatalf("schema %s accepted invalid %s", tc.schema, tc.body)
			}
		})
	}
}

func TestApplicationContentAndExpiryProductionWireConforms(t *testing.T) {
	expiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	resource := sessionprofile.Resource{
		ID: "app-resource-" + strings.Repeat("a", 64), SessionID: "session-1", Name: "chart.png", MediaType: "image/png",
		Size: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: &expiry,
	}
	validateWireValue(t, "SessionResource", resource)
	validateWireValue(t, "SessionResourceRequest", appserver.SessionResourceRequest{
		WriteBase: appserver.WriteBase{OperationID: "upload-image-1"}, Name: "chart.png", MediaType: "image/png", Data: []byte("a"),
		SHA256: strings.Repeat("a", 64), ExpiresAt: &expiry,
	})
	validateWireValue(t, "SessionToolDefinition", sessionprofile.ToolDefinition{
		Name: "ExampleLookup", Description: "Look up value", InputSchema: map[string]any{"type": "object"},
		ResultFormat: "content-v1", OutputSchema: map[string]any{"type": "object"},
	})
	validateWireValue(t, "SessionToolCallResult", sessionprofile.CallResult{
		Outcome: "succeeded", ResultFormat: "content-v1", Content: json.RawMessage(`[{
			"type":"text","text":"ok"},{"type":"resource_link","uri":"application-resource:app-resource-` + strings.Repeat("a", 64) + `","name":"chart.png","mimeType":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]`),
		StructuredContent: map[string]any{"ok": true},
	})
}
