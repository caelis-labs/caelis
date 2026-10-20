package wirev1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/control/uipreferences"
)

// knownWireFields removes additive object properties at the public wire boundary.
// Internal custom decoders still validate every known value, including explicit
// nulls. Opaque maps, interfaces and raw JSON retain their original contents.
// This does not relax disk formats or alter the values passed to Control.
func knownWireFields(raw []byte, target any) ([]byte, error) {
	// Extend this list only with matching schema and production-path tests.
	switch target.(type) {
	case *struct{}, *sessionprofile.CallResult, *TaskDirectorySnapshot,
		*appserver.HostStatus,
		*appserver.SessionState,
		*appserver.PresentationCapabilities,
		*appserver.PresentationSnapshot,
		*appserver.TerminalOutput,
		*appserver.TerminalExitStatus,
		*appserver.AgentCandidate,
		*appserver.AgentStatusSnapshot,
		*appserver.DisconnectCandidatesSnapshot,
		*appserver.CompletionCandidate,
		*appserver.ResumeCandidate,
		*appserver.SlashArgCandidate,
		*appserver.SkillResolveResult,
		*appserver.PluginSnapshot,
		*appserver.MarketplaceSnapshot,
		*appserver.ModelAuthenticationSnapshot,
		*status.StatusSnapshot,
		*agents.ACPPreparation,
		*agentbinding.Status,
		*taskstream.ListResult,
		*FeedDelivery,
		*TaskStreamReadResult,
		*TaskStreamDelivery,
		*[]appserver.AgentCandidate,
		*[]appserver.CompletionCandidate,
		*[]appserver.ResumeCandidate,
		*[]appserver.SlashArgCandidate,
		*[]appserver.PluginSnapshot,
		*[]appserver.MarketplaceSnapshot,
		*appserver.AddMarketplaceRequest,
		*appserver.AddPluginPathRequest,
		*appserver.AgentBindingSetRequest,
		*appserver.AgentRequest,
		*appserver.BindAgentBindingRequest,
		*appserver.CancelParticipantRequest,
		*appserver.CompletionRequest,
		*appserver.ConnectACPRequest,
		*appserver.ConnectModelRequest,
		*appserver.CreateAgentRoleRequest,
		*appserver.DeleteAgentRoleRequest,
		*appserver.DeleteModelRequest,
		*appserver.DisablePluginRequest,
		*appserver.DisconnectACPRequest,
		*appserver.EnablePluginRequest,
		*appserver.HandoffAgentRequest,
		*appserver.InstallPluginRequest,
		*appserver.ModelAuthenticationInput,
		*appserver.PluginRequest,
		*appserver.PrepareACPAuthenticationRequest,
		*appserver.PrepareACPRequest,
		*appserver.PromptParticipantRequest,
		*appserver.RecordInteractionRequest,
		*appserver.RemoveMarketplaceRequest,
		*appserver.RemovePluginRequest,
		*appserver.ResetAgentBindingRequest,
		*appserver.SandboxRequest,
		*appserver.SessionControllerModeRequest,
		*appserver.SessionModeRequest,
		*appserver.SessionModelRequest,
		*appserver.SessionPresentationConfigRequest,
		*appserver.SessionPresentationModeRequest,
		*appserver.StartParticipantRequest,
		*appserver.SubagentInputRequest,
		*appserver.SubagentInputStatusRequest,
		*appserver.TerminalRequest,
		*appserver.UpdateMarketplaceRequest,
		*appserver.UseModelRequest,
		*appserver.WorkspaceTrustRequest,
		*appserver.CreateSessionRequest, *appserver.CloseSessionRequest,
		*appserver.CompactSessionRequest, *appserver.RefreshMCPToolsRequest,
		*appserver.PromptRequest, *appserver.SteerRequest, *appserver.CancelRequest,
		*appserver.ResolveApprovalRequest,
		*appserver.SessionResourceRequest,
		*appserver.CreateSessionBackgroundGrantRequest, *appserver.RevokeSessionBackgroundGrantRequest,
		*sessionprofile.BackgroundGrantRequest,
		*sessionprofile.UpdateConfigurationRequest, *appserver.UpdateSessionConfigurationRequest,
		*appserver.ServerInfo, *appserver.CommandResult,
		*sessionprofile.Configuration, *sessionprofile.Binding,
		**[]sessionprofile.Binding, *sessionprofile.Call, *[]sessionprofile.Call,
		*sessionprofile.Resource, *appserver.SessionResourceContent,
		*sessionprofile.ModelCapabilities, *sessionprofile.ReviewerState, *sessionprofile.MCPStatus,
		*sessionprofile.BackgroundGrant, *[]sessionprofile.BackgroundGrant,

		*uipreferences.Preferences:
		return filterWireFields(raw, reflect.TypeOf(target), 0)
	default:
		return raw, nil
	}
}

func filterWireFields(raw []byte, typ reflect.Type, depth int) ([]byte, error) {
	if depth > 128 {
		return nil, fmt.Errorf("control wire v1: object nesting exceeds limit")
	}
	if typ == nil {
		return raw, nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		fields := wireObjectFields(typ)
		// Custom scalar encodings such as time.Time have no JSON object fields.
		if len(fields) == 0 && typ != reflect.TypeFor[struct{}]() {
			return raw, nil
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		if object == nil {
			return raw, nil
		}
		for name, value := range object {
			field, ok := fields[name]
			if !ok {
				// Preserve encoding/json's existing case-insensitive matching behavior.
				for key, candidate := range fields {
					if strings.EqualFold(key, name) {
						field, ok = candidate, true
						break
					}
				}
			}
			if !ok {
				// Existing forbidden fields are not future optional extensions.
				// Leave them for the original semantic validator to reject.
				if reservedWireField(typ, name) {
					continue
				}
				delete(object, name)
				continue
			}
			filtered, err := filterWireFields(value, field, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = filtered
		}
		return json.Marshal(object)
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return raw, nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
		if values == nil {
			return raw, nil
		}
		for i, value := range values {
			filtered, err := filterWireFields(value, typ.Elem(), depth+1)
			if err != nil {
				return nil, err
			}
			values[i] = filtered
		}
		return json.Marshal(values)
	default:
		return raw, nil
	}
}

func wireObjectFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for _, field := range reflect.VisibleFields(typ) {
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func reservedWireField(typ reflect.Type, name string) bool {
	if typ == reflect.TypeFor[sessionprofile.ConfigurationPatch]() {
		for field := range wireObjectFields(reflect.TypeFor[sessionprofile.Profile]()) {
			if strings.EqualFold(field, name) {
				return true
			}
		}
	}
	if typ == reflect.TypeFor[sessionprofile.MCPServer]() {
		for _, field := range []string{"env", "environment", "headers", "bearer_token"} {
			if strings.EqualFold(field, name) {
				return true
			}
		}
	}
	return false
}
