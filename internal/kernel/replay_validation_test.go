package kernel

import (
	"errors"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestValidateReplaySessionEventsRejectsUnboundedToolOutput(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-1",
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			RawOutput: map[string]any{
				"stderr": strings.Repeat("permission denied\n", tool.DefaultTruncationPolicy().ByteBudget()),
			},
			Content: []session.ProtocolToolCallContent{{Type: "terminal", Content: session.ProtocolTextContent("permission denied")}},
		}},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want non-canonical replay rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Kind != KindValidation || gatewayErr.Code != CodeInvalidRequest {
		t.Fatalf("error = %#v, want validation invalid_request", err)
	}
	if !strings.Contains(gatewayErr.Detail, "tool-1") || !strings.Contains(gatewayErr.Detail, "canonical-truncated") {
		t.Fatalf("error detail = %q, want event id and canonical truncation reason", gatewayErr.Detail)
	}
}

func TestValidateReplaySessionEventsRejectsOutputFieldsInMeta(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-2",
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			RawOutput: map[string]any{"stdout": "ok"},
			Content:   []session.ProtocolToolCallContent{{Type: "terminal", Content: session.ProtocolTextContent("ok")}},
		}},
		Meta: map[string]any{"stderr": "duplicated"},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want meta output rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, `field "stderr"`) {
		t.Fatalf("error = %#v, want stderr meta rejection", err)
	}
}

func TestValidateReplaySessionEventsAllowsCanonicalEscapableOutput(t *testing.T) {
	t.Parallel()

	stdout := strings.Repeat("\n", tool.DefaultTruncationPolicy().ByteBudget()-16)
	if _, info := tool.TruncateMap(map[string]any{"stdout": stdout}, tool.DefaultTruncationPolicy()); info.Truncated {
		t.Fatalf("test fixture unexpectedly exceeds truncation estimator: %#v", info)
	}
	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-3",
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			RawOutput: map[string]any{"stdout": stdout},
			Content:   []session.ProtocolToolCallContent{{Type: "terminal", Content: session.ProtocolTextContent("ok")}},
		}},
	}})
	if err != nil {
		t.Fatalf("validateReplaySessionEvents() error = %v, want canonical escapable output accepted", err)
	}
}

func TestValidateReplaySessionEventsRejectsOldRawOutputOnlyToolResult(t *testing.T) {
	t.Parallel()

	err := validateReplaySessionEvents([]*session.Event{{
		ID:   "tool-old",
		Type: session.EventTypeToolResult,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			RawOutput: map[string]any{"stdout": "ok"},
		}},
	}})
	if err == nil {
		t.Fatal("validateReplaySessionEvents() error = nil, want old rawOutput-only rejection")
	}
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || !strings.Contains(gatewayErr.Detail, "rawOutput-only") {
		t.Fatalf("error = %#v, want rawOutput-only rejection", err)
	}
}
