package tool

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestRejectUnknownArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    map[string]any
		allowed []string
		wantErr string
	}{
		{
			name:    "empty args",
			args:    nil,
			allowed: []string{"path"},
		},
		{
			name:    "allowed arg",
			args:    map[string]any{"path": "notes.txt"},
			allowed: []string{" path ", ""},
		},
		{
			name:    "unknown arg",
			args:    map[string]any{"path": "notes.txt", "unexpected": true},
			allowed: []string{"path"},
			wantErr: "unexpected",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := RejectUnknownArgs(tt.args, tt.allowed...)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("RejectUnknownArgs() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("RejectUnknownArgs() error = nil, want error")
			}
			var toolErr *ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != ErrorCodeInvalidInput {
				t.Fatalf("RejectUnknownArgs() error = %#v, want invalid_input ToolError", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("RejectUnknownArgs() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestErrorPayloadUsesTypedCodeNotMessageGuessing(t *testing.T) {
	t.Parallel()

	typed := ErrorPayload(errorcode.New(errorcode.NotFound, "opaque"))
	if got := typed["error_code"]; got != string(ErrorCodeNotFound) {
		t.Fatalf("typed error_code = %#v, want not_found", got)
	}
	untyped := ErrorPayload(errors.New("file not found"))
	if got := untyped["error_code"]; got != string(ErrorCodeInvalidInput) {
		t.Fatalf("message-only error_code = %#v, want invalid_input", got)
	}
	cancelled := ErrorPayload(context.Canceled)
	if got := cancelled["error_code"]; got != string(ErrorCodeCancelled) {
		t.Fatalf("cancelled error_code = %#v, want cancelled", got)
	}
}

func TestNormalizeCommandSandboxPermission(t *testing.T) {
	t.Parallel()

	if got, err := NormalizeCommandSandboxPermission(CommandSandboxPermissionLegacyAdditional, true); err != nil || got != CommandSandboxPermissionUseDefault {
		t.Fatalf("NormalizeCommandSandboxPermission(legacy,true) = (%q, %v), want use_default nil", got, err)
	}
	if _, err := NormalizeCommandSandboxPermission(CommandSandboxPermissionLegacyAdditional, false); err == nil {
		t.Fatal("NormalizeCommandSandboxPermission(legacy,false) error = nil, want error")
	}
	if got, err := NormalizeCommandSandboxPermission(" "+CommandSandboxPermissionRequireEscalated+" ", false); err != nil || got != CommandSandboxPermissionRequireEscalated {
		t.Fatalf("NormalizeCommandSandboxPermission(require_escalated) = (%q, %v)", got, err)
	}
}
