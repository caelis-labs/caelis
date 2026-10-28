package wirev1

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/control/experimental"
)

func TestExperimentalWireSchemaAndExactRevision(t *testing.T) {
	revision := uint64(math.MaxUint64)
	req := appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "original", ExpectedRevision: &revision}, Values: experimental.Values{experimental.AsyncCompact: false, experimental.MCPRefresh: true}}
	raw, err := Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := openAPIValidator(t, "ExperimentalRequest").Validate(value); err != nil {
		t.Fatal(err)
	}
	var generatedReq generated.ExperimentalRequest
	if err := json.Unmarshal(raw, &generatedReq); err != nil {
		t.Fatal(err)
	}
	if generatedReq.Values.AsyncCompact == nil || *generatedReq.Values.AsyncCompact {
		t.Fatal("generated client lost explicit false")
	}
	var decoded appserver.ExperimentalRequest
	if err := DecodeRequest(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req, decoded) {
		t.Fatal("uint64/request changed")
	}
	snapshot := experimental.Snapshot{Revision: "18446744073709551615", Scope: "host", AppliesAt: "next_runtime_activation", Features: experimental.Definitions(), Desired: experimental.Complete(nil)}
	raw, err = Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := openAPIValidator(t, "ExperimentalSnapshot").Validate(value); err != nil {
		t.Fatal(err)
	}
}

func TestExperimentalCatalogMatchesWireProperties(t *testing.T) {
	raw, err := os.ReadFile("../../../api/control/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type        string
					Deprecated  bool
					Description string
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	properties := spec.Components.Schemas["ExperimentalValues"].Properties
	definitions := experimental.Definitions()
	if len(properties) != len(definitions)+2 {
		t.Fatal("wire and catalog feature sets differ")
	}
	for _, id := range []string{experimental.MCPRefresh, experimental.ToolRetirement} {
		property := properties[id]
		if property.Type != "boolean" || !property.Deprecated || !strings.Contains(property.Description, "ignored") {
			t.Fatalf("legacy property %q = %+v", id, property)
		}
	}
	for _, feature := range definitions {
		if properties[feature.ID].Type != "boolean" {
			t.Fatalf("wire feature %q is missing or not boolean", feature.ID)
		}
		for _, enabled := range []bool{false, true} {
			if err := experimental.Validate(experimental.Values{feature.ID: enabled}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestExperimentalMCPRefreshWire(t *testing.T) {
	revision := uint64(math.MaxUint64)
	request := appserver.RefreshMCPToolsRequest{WriteBase: appserver.WriteBase{OperationID: "original", SessionID: "owned", ExpectedRevision: &revision, ExpectedControllerEpoch: "epoch"}, PluginID: "application", ServerName: "documents"}
	raw, err := Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err = openAPIValidator(t, "RefreshMCPToolsRequest").Validate(value); err != nil {
		t.Fatal(err)
	}
	var decoded appserver.RefreshMCPToolsRequest
	if err = DecodeRequest(raw, &decoded); err != nil || !reflect.DeepEqual(request, decoded) {
		t.Fatalf("receipt changed in wire roundtrip: %+v %v", decoded, err)
	}
	var generatedRequest generated.RefreshMCPToolsRequest
	if err = json.Unmarshal(raw, &generatedRequest); err != nil || generatedRequest.PluginId != "application" {
		t.Fatalf("generated refresh request=%+v %v", generatedRequest, err)
	}
}

func TestMCPCatalogRefreshFactsAreOptionalBooleans(t *testing.T) {
	raw, err := os.ReadFile("../../../api/control/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string
				Properties map[string]struct{ Type string }
			}
		}
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MCPServerSnapshot", "SessionMCPServerStatus"} {
		schema := spec.Components.Schemas[name]
		for _, field := range []string{"refreshing", "stale", "refresh_failed"} {
			if schema.Properties[field].Type != "boolean" {
				t.Fatalf("%s.%s must be boolean", name, field)
			}
			for _, required := range schema.Required {
				if required == field {
					t.Fatalf("%s.%s must be optional", name, field)
				}
			}
		}
	}
}
