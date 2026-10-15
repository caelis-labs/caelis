package wirev1

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/status"
)

func TestAdditiveRequestFieldsPreserveKnownValues(t *testing.T) {
	raw := []byte(`{"operation_id":"original","expected_revision":"9007199254740993","future":{"counter":99999999999999999999},"execution_config":{"future":true,"environment":{"inherit":false,"set":{"KEEP":"value"},"future":null},"shell":{"path":"/bin/sh","future":true}}}`)
	var request appserver.CreateSessionRequest
	if err := DecodeRequest(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.OperationID != "original" || request.ExpectedRevision == nil || *request.ExpectedRevision != 9007199254740993 || request.ExecutionConfig == nil || request.ExecutionConfig.Environment.Inherit == nil || *request.ExecutionConfig.Environment.Inherit || request.ExecutionConfig.Environment.Set["KEEP"] != "value" || request.ExecutionConfig.Shell.Path != "/bin/sh" {
		t.Fatalf("known values changed: %+v", request)
	}
	for _, raw := range []string{
		`{"operation_id":false,"future":true}`,
		`{"expected_revision":9007199254740993,"future":true}`,
		`{"execution_config":{"environment":{"set":{"KEY":null},"future":true}}}`,
		`{"execution_config":{"environment":{"inherit":"false","future":true}}}`,
		`{"operation_id":"first"} {"operation_id":"second"}`,
	} {
		if err := DecodeRequest([]byte(raw), &appserver.CreateSessionRequest{}); err == nil {
			t.Fatalf("accepted invalid known value: %s", raw)
		}
	}
}

func TestAdditiveApplicationFieldsPreservePatchAndOpaqueSchema(t *testing.T) {
	raw := []byte(`{"operation_id":"original","expected_configuration_revision":"7","future":true,"patch":{"native_tools":[],"future":null,"mcp_servers":[{"name":"test","transport":"stdio","command":"/bin/echo","future":true}]}}`)
	var request application.UpdateConfigurationRequest
	if err := DecodeRequest(raw, &request); err != nil {
		t.Fatal(err)
	}
	if request.ExpectedConfigurationRevision != 7 || request.Patch.NativeTools == nil || len(*request.Patch.NativeTools) != 0 || request.Patch.MCPServers == nil || (*request.Patch.MCPServers)[0].Command != "/bin/echo" {
		t.Fatalf("patch changed: %+v", request)
	}
	if err := DecodeRequest([]byte(`{"patch":{"native_tools":null,"future":true}}`), &application.UpdateConfigurationRequest{}); err == nil {
		t.Fatal("known null accepted")
	}
	var patch application.UpdateConfigurationRequest
	raw = []byte(`{"patch":{"tools":[{"name":"tool","input_schema":{"properties":{"future":{"type":"string"}}}}]}}`)
	if err := DecodeRequest(raw, &patch); err != nil {
		t.Fatal(err)
	}
	schema, _ := json.Marshal((*patch.Patch.Tools)[0].InputSchema)
	if string(schema) != `{"properties":{"future":{"type":"string"}}}` {
		t.Fatalf("opaque schema changed: %s", schema)
	}
}

func TestAdditiveResponseFieldsPreserveReceipt(t *testing.T) {
	var result appserver.CommandResult
	raw := []byte(`{"operation_id":"original","outcome":"unknown","revision":"18446744073709551615","future":true,"target":{"turn_id":"original-turn","future":true}}`)
	if err := Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.OperationID != "original" || result.Outcome != appserver.OutcomeUnknown || result.Revision != ^uint64(0) || result.Target.TurnID != "original-turn" {
		t.Fatalf("receipt changed: %+v", result)
	}
	if err := Unmarshal([]byte(`{"revision":"invalid","future":true}`), &result); err == nil {
		t.Fatal("invalid known revision accepted")
	}
}

func TestAdditiveFieldsDoNotHideForbiddenKnownSettings(t *testing.T) {
	for _, raw := range []string{
		`{"patch":{"workspace":{"cwd":"/tmp"},"future":true}}`,
		`{"patch":{"permissions":{},"future":true}}`,
		`{"patch":{"execution":"full-access","future":true}}`,
		`{"patch":{"mcp_servers":[{"name":"test","transport":"stdio","command":"/bin/echo","headers":{"Authorization":"secret"},"future":true}]}}`,
	} {
		if err := DecodeRequest([]byte(raw), &application.UpdateConfigurationRequest{}); err == nil {
			t.Fatalf("forbidden setting accepted: %s", raw)
		}
	}
}

func TestAdditiveSchemaKeepsKnownValidation(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"CreateSessionRequest", `{"operation_id":"op","future":true,"execution_config":{"environment":{"inherit":false,"future":true},"shell":{"path":"/bin/sh","future":true},"future":true}}`},
		{"UpdateApplicationConfigurationRequest", `{"operation_id":"op","expected_configuration_revision":"7","patch":{"native_tools":[],"future":null},"future":true}`},
		{"ServerInfo", `{"protocol_version":1,"envelope_version":"caelis.control.envelope/v1","api_version":"v1","future":true}`},
		{"CommandResult", `{"operation_id":"op","outcome":"unknown","future":true,"target":{"handle_id":"h","run_id":"r","turn_id":"t","future":true}}`},
		{"UIPreferences", `{"theme":"auto","future":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := openAPIValidator(t, tc.name).Validate(decodeJSONWithNumbers(t, []byte(tc.raw))); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct{ name, raw string }{
		{"ApplicationConfigurationPatch", `{"permissions":{},"future":true}`},
		{"ApplicationConfigurationPatch", `{"workspace":{},"future":true}`},
		{"ApplicationConfigurationPatch", `{"native_tools":null,"future":true}`},
		{"ApplicationMCPServer", `{"name":"m","transport":"stdio","command":"/bin/echo","headers":{},"future":true}`},
		{"CommandResult", `{"operation_id":"op","outcome":"invented","future":true}`},
		{"EnvironmentConfig", `{"set":{"KEY":null},"future":true}`},
	} {
		if err := openAPIValidator(t, tc.name).Validate(decodeJSONWithNumbers(t, []byte(tc.raw))); err == nil {
			t.Fatalf("accepted invalid %s: %s", tc.name, tc.raw)
		}
	}
}

// These are the shared command families and Application adapters whose wire
// boundary is additive. Domain validation still runs after decoding.
func TestAdditiveControlCommandFamilies(t *testing.T) {
	base := appserver.WriteBase{OperationID: "original", SessionID: "session"}
	target := appserver.TurnTarget{TurnID: "turn", RunID: "run", HandleID: "handle"}
	for _, value := range []any{

		appserver.AddMarketplaceRequest{},
		appserver.AddPluginPathRequest{},
		appserver.AgentBindingSetRequest{},
		appserver.AgentRequest{},
		appserver.BindAgentBindingRequest{},
		appserver.CancelParticipantRequest{},
		appserver.CompletionRequest{},
		appserver.ConnectACPRequest{},
		appserver.ConnectModelRequest{},
		appserver.CreateAgentRoleRequest{},
		appserver.DeleteAgentRoleRequest{},
		appserver.DeleteModelRequest{},
		appserver.DisablePluginRequest{},
		appserver.DisconnectACPRequest{},
		appserver.EnablePluginRequest{},
		appserver.HandoffAgentRequest{},
		appserver.InstallPluginRequest{},
		appserver.ModelAuthenticationInput{},
		appserver.PluginRequest{},
		appserver.PrepareACPAuthenticationRequest{},
		appserver.PrepareACPRequest{},
		appserver.PromptParticipantRequest{},
		appserver.RecordInteractionRequest{},
		appserver.RemoveMarketplaceRequest{},
		appserver.RemovePluginRequest{},
		appserver.ResetAgentBindingRequest{},
		appserver.SandboxRequest{},
		appserver.SessionControllerModeRequest{},
		appserver.SessionModeRequest{},
		appserver.SessionModelRequest{},
		appserver.SessionPresentationConfigRequest{},
		appserver.SessionPresentationModeRequest{},
		appserver.StartParticipantRequest{},
		appserver.SubagentInputRequest{},
		appserver.SubagentInputStatusRequest{},
		appserver.TerminalRequest{},
		appserver.UpdateMarketplaceRequest{},
		appserver.UseModelRequest{},
		appserver.WorkspaceTrustRequest{},

		appserver.CloseSessionRequest{WriteBase: base},
		appserver.CompactSessionRequest{WriteBase: base},
		appserver.PromptRequest{WriteBase: base, Input: "original"},
		appserver.SteerRequest{WriteBase: base, Target: target, Input: "original"},
		appserver.CancelRequest{WriteBase: base, Target: target, Reason: "original"},
		appserver.ResolveApprovalRequest{WriteBase: base, Target: target, ApprovalRequestID: "approval", Outcome: "cancelled"},
		appserver.CreateWorkerRequest{WriteBase: base, CWD: "/tmp"},
		appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: base, Input: "original"}, SourceKind: "user"},
		appserver.ApplicationResourceRequest{WriteBase: base, Name: "original", Data: []byte("original")},
		application.Registration{OperationID: "original", Name: "test", Credential: "test"},
		application.BackgroundGrantRequest{OperationID: "original", Source: "test", AuthorizationOperationID: "authorization"},
	} {
		t.Run(reflect.TypeOf(value).Name(), func(t *testing.T) {
			raw, err := Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["future_optional"] = json.RawMessage(`{"nested":99999999999999999999}`)
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			got := reflect.New(reflect.TypeOf(value)).Interface()
			if err := DecodeRequest(raw, got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reflect.ValueOf(got).Elem().Interface(), value) {
				t.Fatalf("known fields changed: %#v", got)
			}
		})
	}
}

func TestAdditiveApplicationCreationPreservesProfileAuthority(t *testing.T) {
	raw, err := os.ReadFile("../../../api/control/v1/fixtures/application-guardian-create.json")
	if err != nil {
		t.Fatal(err)
	}
	var original appserver.CreateApplicationSessionRequest
	if err := DecodeRequest(raw, &original); err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(body["profile"], &profile); err != nil {
		t.Fatal(err)
	}
	profile["future_optional"] = json.RawMessage(`true`)
	// Keep the fixture's existing reviewer choice while adding an extension.
	var reviewer map[string]json.RawMessage
	if err := json.Unmarshal(profile["reviewer"], &reviewer); err != nil {
		t.Fatal(err)
	}
	reviewer["future_optional"] = json.RawMessage(`true`)
	profile["reviewer"], _ = json.Marshal(reviewer)
	body["profile"], _ = json.Marshal(profile)
	body["future_optional"] = json.RawMessage(`true`)
	raw, _ = json.Marshal(body)
	var got appserver.CreateApplicationSessionRequest
	if err := DecodeRequest(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("creation authority changed: %#v", got)
	}
	if err := openAPIValidator(t, "CreateApplicationSessionRequest").Validate(decodeJSONWithNumbers(t, raw)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"profile":{"native_tools":null,"future_optional":true}}`,
		`{"profile":{"inherit":{"workspace_memory":"true"},"future_optional":true}}`,
		`{"profile":{"permissions":{"approval_mode":false},"future_optional":true}}`,
		`{"profile":{"mcp_servers":[{"env":{"SECRET":"value"},"future_optional":true}]}}`,
	} {
		if err := DecodeRequest([]byte(invalid), &appserver.CreateApplicationSessionRequest{}); err == nil {
			t.Fatalf("accepted invalid authority: %s", invalid)
		}
	}
}

func TestAdditiveControlResponseFamilies(t *testing.T) {
	for _, value := range []any{
		application.Connection{Name: "original"},
		application.Configuration{SessionID: "session", Revision: 9007199254740993, Profile: application.Profile{NativeTools: []string{}}},
		application.Binding{SessionID: "session", CreationDigest: "original", Profile: application.Profile{NativeTools: []string{}}},
		[]application.Worker{{SessionID: "original"}},
		[]application.Binding{{SessionID: "session", CreationDigest: "original"}},
		application.Call{ID: "original", Arguments: json.RawMessage(`{"future_optional":"business input"}`)},
		[]application.Call{{ID: "original", Arguments: json.RawMessage(`{"future_optional":"business input"}`)}},
		application.Resource{ID: "original", SessionID: "session", SHA256: "original"},
		appserver.ApplicationResourceContent{Resource: application.Resource{ID: "original"}, Data: []byte("original")},
		application.ModelCapabilities{SessionID: "session", ConfigurationRevision: 9007199254740993},
		application.ReviewerState{SessionID: "session"},
		application.MCPStatus{SessionID: "session"},
		application.BackgroundGrant{ID: "original"},
		[]application.BackgroundGrant{{ID: "original"}},
		appserver.HostStatus{},
		appserver.SessionState{},
		appserver.PresentationCapabilities{},
		appserver.PresentationSnapshot{},
		appserver.TerminalOutput{},
		appserver.TerminalExitStatus{},
		appserver.AgentCandidate{},
		appserver.AgentStatusSnapshot{},
		appserver.DisconnectCandidatesSnapshot{},
		appserver.CompletionCandidate{},
		appserver.ResumeCandidate{},
		appserver.SlashArgCandidate{},
		appserver.SkillResolveResult{},
		appserver.PluginSnapshot{},
		appserver.MarketplaceSnapshot{},
		appserver.ModelAuthenticationSnapshot{},
		status.StatusSnapshot{},
		agents.ACPPreparation{},
		agentbinding.Status{},
		taskstream.ListResult{},
		FeedDelivery{},
		TaskDirectorySnapshot{Revision: "9007199254740993"},
		TaskStreamReadResult{},
		TaskStreamDelivery{},
		[]appserver.AgentCandidate{{}},
		[]appserver.CompletionCandidate{{}},
		[]appserver.ResumeCandidate{{}},
		[]appserver.SlashArgCandidate{{}},
		[]appserver.PluginSnapshot{{}},
		[]appserver.MarketplaceSnapshot{{}},
		appserver.ApplicationOperation{OperationID: "original", Outcome: appserver.OutcomeUnknown, Result: &appserver.CommandResult{OperationID: "original", Outcome: appserver.OutcomeUnknown}},
	} {
		t.Run(reflect.TypeOf(value).String(), func(t *testing.T) {
			raw, err := Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			original := reflect.New(reflect.TypeOf(value)).Interface()
			if err := Unmarshal(raw, original); err != nil {
				t.Fatal(err)
			}
			add := func(data json.RawMessage) json.RawMessage {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				fields["future_optional"] = json.RawMessage(`{"not_authority":true}`)
				result, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			if reflect.TypeOf(value).Kind() == reflect.Slice {
				var items []json.RawMessage
				if err := json.Unmarshal(raw, &items); err != nil {
					t.Fatal(err)
				}
				for i := range items {
					items[i] = add(items[i])
				}
				raw, err = json.Marshal(items)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				raw = add(raw)
			}
			got := reflect.New(reflect.TypeOf(value)).Interface()
			if err := Unmarshal(raw, got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("receipt changed: got %#v want %#v", got, original)
			}
		})
	}
}

func TestAdditiveEmptyAndCallbackBodies(t *testing.T) {
	var empty struct{}
	if err := DecodeRequest([]byte(`{"future_optional":true}`), &empty); err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal([]byte(`{"future_optional":true}`), &empty); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`[]`, `"unexpected"`, `true`} {
		if err := DecodeRequest([]byte(raw), &empty); err == nil {
			t.Fatalf("accepted nonobject: %s", raw)
		}
	}
	var result application.CallResult
	if err := DecodeRequest([]byte(`{"outcome":"succeeded","content":{"business_future":true},"future_optional":true}`), &result); err != nil {
		t.Fatal(err)
	}
	if string(result.Content) != `{"business_future":true}` || result.Outcome != "succeeded" {
		t.Fatalf("callback payload changed: %+v", result)
	}
	for _, raw := range []string{
		`{"outcome":true,"content":null,"future_optional":true}`,
		`{"outcome":"succeeded","content":null,"turn_complete":"true","future_optional":true}`,
	} {
		if err := DecodeRequest([]byte(raw), &application.CallResult{}); err == nil {
			t.Fatalf("accepted invalid callback field: %s", raw)
		}
	}
	for _, name := range []string{"EmptyResult", "ApplicationEmptyRequest"} {
		if err := openAPIValidator(t, name).Validate(decodeJSONWithNumbers(t, []byte(`{"future_optional":true}`))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdditiveFeedPositionKeepsExclusiveLanes(t *testing.T) {
	validator := openAPIValidator(t, "FeedPosition")
	for _, raw := range []string{
		`{"durable":{"seq":"9007199254740993","projection_index":0,"future_optional":true},"future_optional":true}`,
		`{"transient":{"anchor":{"seq":"7","projection_index":0,"future_optional":true},"generation":"instance","sequence":"1","future_optional":true},"future_optional":true}`,
	} {
		if err := validator.Validate(decodeJSONWithNumbers(t, []byte(raw))); err != nil {
			t.Fatal(err)
		}
		var state appserver.SessionState
		if err := Unmarshal([]byte(`{"revision":"7","boundary_position":`+raw+`}`), &state); err != nil {
			t.Fatal(err)
		}
		if state.BoundaryPosition == nil || state.BoundaryPosition.Validate() != nil {
			t.Fatalf("invalid decoded position: %+v", state.BoundaryPosition)
		}
	}
	for _, raw := range []string{
		`{"future_optional":true}`,
		`{"durable":{"seq":"7","projection_index":0},"transient":{"anchor":{"seq":"7","projection_index":0},"generation":"instance","sequence":"1"},"future_optional":true}`,
	} {
		if err := validator.Validate(decodeJSONWithNumbers(t, []byte(raw))); err == nil {
			t.Fatalf("accepted invalid lanes: %s", raw)
		}
	}
}
