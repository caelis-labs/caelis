package wirev1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
)

func TestExecutionConfigurationSchemaAndProductionWire(t *testing.T) {
	inherit := false
	config := &sandbox.ExecutionConfig{
		Environment: sandbox.EnvironmentConfig{Inherit: &inherit, Set: map[string]string{"A": "custom"}, Unset: []string{"B"}},
		Shell:       sandbox.ShellConfig{Path: "/bin/bash", Login: true},
	}
	ordinary := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "ordinary"}, CWD: "/tmp/workspace", ExecutionConfig: config}
	validateWireValue(t, "CreateSessionRequest", ordinary)
	var wireOrdinary generated.CreateSessionRequest
	decodeGeneratedExecution(t, ordinary, &wireOrdinary)
	if wireOrdinary.ExecutionConfig == nil || wireOrdinary.ExecutionConfig.Environment == nil || wireOrdinary.ExecutionConfig.Environment.Inherit == nil || *wireOrdinary.ExecutionConfig.Environment.Inherit || wireOrdinary.ExecutionConfig.Environment.Set["A"] != "custom" || wireOrdinary.ExecutionConfig.Shell == nil || wireOrdinary.ExecutionConfig.Shell.Login == nil || !*wireOrdinary.ExecutionConfig.Shell.Login {
		t.Fatalf("generated ordinary DTO lost config: %+v", wireOrdinary.ExecutionConfig)
	}
	profile := application.Profile{Version: "native/1", Instructions: "native", Model: "configured", ToolsVersion: "native/1", Execution: "workspace-write", ExecutionConfig: config}
	validateWireValue(t, "ApplicationProfile", profile)
	var wireProfile generated.ApplicationProfile
	decodeGeneratedExecution(t, profile, &wireProfile)
	if wireProfile.ExecutionConfig == nil || wireProfile.ExecutionConfig.Environment == nil || !reflect.DeepEqual(wireProfile.ExecutionConfig.Environment.Unset, []string{"B"}) {
		t.Fatalf("generated application DTO lost config: %+v", wireProfile.ExecutionConfig)
	}
	state := appserver.SessionState{ProtocolVersion: 1, EnvelopeVersion: appserver.EnvelopeVersion, APIVersion: appserver.HTTPAPIVersion, SessionID: "session-1", Revision: 1, ExecutionConfig: config, Run: appserver.RunState{}, Approval: appserver.ApprovalState{}, Capabilities: appserver.ClientCapabilities{CaelisTerminalStream: true}}
	validateWireValue(t, "SessionState", state)
	var wireState generated.SessionState
	decodeGeneratedExecution(t, state, &wireState)
	if wireState.ExecutionConfig == nil || wireState.ExecutionConfig.Environment == nil || wireState.ExecutionConfig.Environment.Set["A"] != "custom" {
		t.Fatalf("generated SessionState DTO lost config: %+v", wireState.ExecutionConfig)
	}
}

func TestExecutionConfigurationNullEmptyAndMissingDefaults(t *testing.T) {
	for name, raw := range map[string]string{
		"missing":            `{"operation_id":"create"}`,
		"null":               `{"operation_id":"create","execution_config":null}`,
		"nested_null":        `{"operation_id":"create","execution_config":{"environment":null,"shell":null}}`,
		"shell_null_scalars": `{"operation_id":"create","execution_config":{"shell":{"path":null,"login":null}}}`,
		"empty":              `{"operation_id":"create","execution_config":{"environment":{"inherit":null,"set":null,"unset":null},"shell":{}}}`,
		"empty_collections":  `{"operation_id":"create","execution_config":{"environment":{"set":{},"unset":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := openAPIValidator(t, "CreateSessionRequest").Validate(decodeJSONWithNumbers(t, []byte(raw))); err != nil {
				t.Fatalf("schema rejects default shape: %v", err)
			}
			var decoded appserver.CreateSessionRequest
			if err := DecodeRequest(json.RawMessage(raw), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.ExecutionConfig != nil {
				if decoded.ExecutionConfig.Environment.Inherit != nil || len(decoded.ExecutionConfig.Environment.Set) != 0 || len(decoded.ExecutionConfig.Environment.Unset) != 0 || decoded.ExecutionConfig.Shell.Path != "" || decoded.ExecutionConfig.Shell.Login {
					t.Fatalf("defaults changed: %+v", decoded.ExecutionConfig)
				}
			}
			validateWireValue(t, "CreateSessionRequest", decoded)
		})
	}
}

func TestExecutionConfigurationRejectsNullSetMemberOnPublicWire(t *testing.T) {
	raw := []byte(`{"operation_id":"create","execution_config":{"environment":{"set":{"A":null}}}}`)
	if err := openAPIValidator(t, "CreateSessionRequest").Validate(decodeJSONWithNumbers(t, raw)); err == nil {
		t.Fatal("OpenAPI accepted a null environment variable value")
	}
	var request appserver.CreateSessionRequest
	if err := DecodeRequest(raw, &request); err == nil {
		t.Fatal("public request decoder accepted a null environment variable value")
	}
}

func decodeGeneratedExecution(t *testing.T, source, target any) {
	t.Helper()
	raw, err := Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
