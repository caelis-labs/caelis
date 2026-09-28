package wirev1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestApplicationContentFixturesConformToOpenAPI(t *testing.T) {
	for _, fixture := range []struct{ file, schema string }{
		{"application-create.json", "CreateApplicationSessionRequest"},
		{"application-guardian-create.json", "CreateApplicationSessionRequest"},
		{"application-reviewer-state.json", "ApplicationReviewerState"},
		{"application-tool-content-v1.json", "ApplicationToolDefinition"},
		{"application-result-legacy.json", "ApplicationCallResult"},
		{"application-result-legacy-text.json", "ApplicationCallResult"},
		{"application-result-content-v1.json", "ApplicationCallResult"},
		{"application-result-empty-structured.json", "ApplicationCallResult"},
		{"application-resource-expiring.json", "ApplicationResourceRequest"},
		{"application-resource-expiring-descriptor.json", "ApplicationResource"},
	} {
		t.Run(fixture.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("../../../api/control/v1/fixtures", fixture.file))
			if err != nil {
				t.Fatal(err)
			}
			if err := openAPIValidator(t, fixture.schema).Validate(decodeJSONWithNumbers(t, raw)); err != nil {
				t.Fatalf("%s: %v", fixture.file, err)
			}
			if fixture.schema == "ApplicationResourceRequest" {
				var request generated.ApplicationResourceRequest
				if err := json.Unmarshal(raw, &request); err != nil || request.ExpiresAt == nil || !request.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("generated Go lost resource request expiration: %#v, %v", request, err)
				}
				return
			}
			if fixture.schema == "ApplicationResource" {
				var resource generated.ApplicationResource
				if err := json.Unmarshal(raw, &resource); err != nil || resource.ExpiresAt == nil || !resource.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
					t.Fatalf("generated Go lost resource descriptor expiration: %#v, %v", resource, err)
				}
				return
			}
			if fixture.schema != "ApplicationCallResult" {
				return
			}
			// Generated Go must preserve both opaque legacy content and new typed
			// block content; the TypeScript generated DTO is a discriminated union.
			var result generated.ApplicationCallResult
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
	result := generated.ApplicationContentCallResult{Outcome: "succeeded", ResultFormat: "content-v1", Content: []generated.ApplicationResultContentBlock{}, StructuredContent: generated.JSONObject{}}
	raw, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), `"structuredContent":{}`) {
		t.Fatalf("generated client lost an empty structured object: %s %v", raw, err)
	}
}

func TestApplicationContentSchemaRejectsInvalidFormatsAndBlocks(t *testing.T) {
	cases := []struct{ name, schema, body string }{
		{"unknown-tool-format", "ApplicationToolDefinition", `{"name":"t","description":"t","input_schema":{},"result_format":"future"}`},
		{"legacy-output-schema", "ApplicationToolDefinition", `{"name":"t","description":"t","input_schema":{},"output_schema":{}}`},
		{"unknown-result-format", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"future","content":[]}`},
		{"legacy-structured-content", "ApplicationCallResult", `{"outcome":"succeeded","content":"ok","structuredContent":{}}`},
		{"not-block-array", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":"text"}`},
		{"unexpected-block", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"audio","data":"YQ=="}]}`},
		{"gif", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"image","data":"YQ==","mimeType":"image/gif"}]}`},
		{"file-uri", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"resource_link","uri":"file:///tmp/a.png","name":"a.png","mimeType":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]}`},
		{"too-many-blocks", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[` + strings.Repeat(`{"type":"text","text":"x"},`, 64) + `{"type":"text","text":"x"}]}`},
		{"text-block-over-model-budget", "ApplicationCallResult", `{"outcome":"succeeded","result_format":"content-v1","content":[{"type":"text","text":"` + strings.Repeat("x", 32769) + `"}]}`},
		{"invalid-expiry", "ApplicationResourceRequest", `{"operation_id":"upload-1","name":"image.png","media_type":"image/png","data":"YQ==","sha256":"` + strings.Repeat("a", 64) + `","expires_at":123}`},
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
	resource := application.Resource{
		ID: "app-resource-" + strings.Repeat("a", 64), SessionID: "session-1", Name: "chart.png", MediaType: "image/png",
		Size: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: &expiry,
	}
	validateWireValue(t, "ApplicationResource", resource)
	validateWireValue(t, "ApplicationResourceRequest", appserver.ApplicationResourceRequest{
		WriteBase: appserver.WriteBase{OperationID: "upload-image-1"}, Name: "chart.png", MediaType: "image/png", Data: []byte("a"),
		SHA256: strings.Repeat("a", 64), ExpiresAt: &expiry,
	})
	validateWireValue(t, "ApplicationToolDefinition", application.ToolDefinition{
		Name: "ExampleLookup", Description: "Look up value", InputSchema: map[string]any{"type": "object"},
		ResultFormat: "content-v1", OutputSchema: map[string]any{"type": "object"},
	})
	validateWireValue(t, "ApplicationCallResult", application.CallResult{
		Outcome: "succeeded", ResultFormat: "content-v1", Content: json.RawMessage(`[{
			"type":"text","text":"ok"},{"type":"resource_link","uri":"application-resource:app-resource-` + strings.Repeat("a", 64) + `","name":"chart.png","mimeType":"image/png","sha256":"` + strings.Repeat("a", 64) + `"}]`),
		StructuredContent: map[string]any{"ok": true},
	})
}
