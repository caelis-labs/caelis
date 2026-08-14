package controlclient

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestPluginSourceCredentialsAreRejectedBeforeOperationBegin(t *testing.T) {
	revision := uint64(1)
	tests := []struct {
		name   string
		invoke func(*CommandService) (CommandResult, error)
	}{
		{
			name: "SSH password",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.AddMarketplace(context.Background(), Principal{ID: "owner"}, AddMarketplaceRequest{
					WriteBase: WriteBase{OperationID: "ssh-password", ExpectedRevision: &revision},
					Source:    "ssh://git:super-secret@example.com/acme/plugins.git",
				})
			},
		},
		{
			name: "HTTPS password with empty username",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.AddMarketplace(context.Background(), Principal{ID: "owner"}, AddMarketplaceRequest{
					WriteBase: WriteBase{OperationID: "https-password", ExpectedRevision: &revision},
					Source:    "https://:super-secret@example.com/acme/plugins.git",
				})
			},
		},
		{
			name: "HTTPS query credential",
			invoke: func(service *CommandService) (CommandResult, error) {
				return service.InstallPlugin(context.Background(), Principal{ID: "owner"}, InstallPluginRequest{
					WriteBase: WriteBase{OperationID: "https-query", ExpectedRevision: &revision},
					Source:    "https://example.com/acme/plugin.git?token=super-secret",
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, operations, backend)

			result, err := test.invoke(service)
			if err == nil || result.Outcome != OutcomeRejected || errorcode.CodeOf(err) != errorcode.InvalidArgument {
				t.Fatalf("plugin command = %#v, %v; want invalid-argument rejection", result, err)
			}
			if operations.beginCalls != 0 || len(operations.intents) != 0 || backend.calls != 0 {
				t.Fatalf("durable/effect calls = begin %d intents %d backend %d, want all zero", operations.beginCalls, len(operations.intents), backend.calls)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("error leaked credential: %v", err)
			}
		})
	}
}

func TestPluginSourceIntentTargetIsOpaqueAndStable(t *testing.T) {
	revision := uint64(1)
	operations := &countingOperationStore{OperationStore: NewMemoryOperationStore()}
	backend := &recordingCommandBackend{}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
	source := "ssh://git@example.com/acme/plugins.git"

	for _, operationID := range []string{"opaque-source-1", "opaque-source-2"} {
		result, err := service.AddMarketplace(context.Background(), Principal{ID: "owner"}, AddMarketplaceRequest{
			WriteBase: WriteBase{OperationID: operationID, ExpectedRevision: &revision},
			Source:    source,
		})
		if err != nil || result.Outcome != OutcomeCommitted {
			t.Fatalf("AddMarketplace(%s) = %#v, %v", operationID, result, err)
		}
	}
	if len(operations.intents) != 2 {
		t.Fatalf("intents = %d, want 2", len(operations.intents))
	}
	first := operations.intents[0].Target
	second := operations.intents[1].Target
	if first == "" || first != second || strings.Contains(first, source) || !strings.Contains(first, "/sha256/") {
		t.Fatalf("targets = %q / %q, want stable opaque source identity", first, second)
	}
}
