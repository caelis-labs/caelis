package providers

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestExtractResponsesStreamErrorPreservesTypeCodeAndMessage(t *testing.T) {
	t.Parallel()

	var event openAICodexStreamWire
	if err := json.Unmarshal([]byte(
		`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid field"}}}`,
	), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	want := responsesStreamErrorDetails{
		errorType: "invalid_request_error",
		code:      "invalid_value",
		message:   "Invalid field",
	}
	if got := extractResponsesStreamError(event); got != want {
		t.Fatalf("extractResponsesStreamError() = %#v, want %#v", got, want)
	}
}

func TestOpenAICodexStreamErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		event            string
		wantCode         errorcode.Code
		wantRetryable    bool
		wantBackpressure bool
	}{
		{
			name:          "missing provider detail",
			event:         `{"type":"response.failed","response":{"status":"failed","error":null}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:          "alternate top-level error envelope",
			event:         `{"type":"response.failed","response":{"status":"failed","error":null},"error":{"type":"server_error","message":"Upstream provider error"}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:             "rate limit",
			event:            `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached"}}}`,
			wantCode:         errorcode.RateLimited,
			wantRetryable:    true,
			wantBackpressure: true,
		},
		{
			name:             "slow down",
			event:            `{"type":"response.failed","response":{"error":{"code":"slow_down","message":"Try again later"}}}`,
			wantCode:         errorcode.Overloaded,
			wantRetryable:    true,
			wantBackpressure: true,
		},
		{
			name:          "request timeout",
			event:         `{"type":"error","code":"request_timeout","message":"Upstream timed out"}`,
			wantCode:      errorcode.Timeout,
			wantRetryable: true,
		},
		{
			name:          "authentication service unavailable overrides terminal type",
			event:         `{"type":"error","error":{"type":"authentication_error","code":"auth_service_unavailable","message":"Authentication service is unavailable"}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:          "websocket connection limit overrides invalid request type",
			event:         `{"type":"error","error":{"type":"invalid_request_error","code":"websocket_connection_limit_reached","message":"Create a new connection"}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:          "invalid request type and code",
			event:         `{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid field"}}}`,
			wantCode:      errorcode.InvalidArgument,
			wantRetryable: false,
		},
		{
			name:          "invalid request type without code",
			event:         `{"type":"response.failed","response":{"error":{"type":"invalid_request_error","message":"Missing required parameter"}}}`,
			wantCode:      errorcode.InvalidArgument,
			wantRetryable: false,
		},
		{
			name:          "billing hard limit",
			event:         `{"type":"response.failed","response":{"error":{"code":"billing_hard_limit_reached","message":"Check billing"}}}`,
			wantCode:      errorcode.ResourceExhausted,
			wantRetryable: false,
		},
		{
			name:          "permissions error",
			event:         `{"type":"response.failed","response":{"error":{"code":"permissions_error","message":"Access denied"}}}`,
			wantCode:      errorcode.PermissionDenied,
			wantRetryable: false,
		},
		{
			name:          "unrelated prose does not select backpressure",
			event:         `{"type":"response.failed","response":{"error":{"code":"novel_gateway_error","message":"This is not a rate limit and the service is not overloaded"}}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:          "quota exceeded",
			event:         `{"type":"response.failed","response":{"error":{"code":"insufficient_quota","message":"Check billing"}}}`,
			wantCode:      errorcode.ResourceExhausted,
			wantRetryable: false,
		},
		{
			name:          "usage not included",
			event:         `{"type":"response.failed","response":{"error":{"code":"usage_not_included","message":"Upgrade plan"}}}`,
			wantCode:      errorcode.FailedPrecondition,
			wantRetryable: false,
		},
		{
			name:          "usage limit reached",
			event:         `{"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			wantCode:      errorcode.ResourceExhausted,
			wantRetryable: false,
		},
		{
			name:          "cyber policy",
			event:         `{"type":"response.failed","response":{"error":{"code":"cyber_policy","message":"Request blocked"}}}`,
			wantCode:      errorcode.PermissionDenied,
			wantRetryable: false,
		},
		{
			name:          "invalid prompt",
			event:         `{"type":"response.failed","response":{"error":{"code":"invalid_prompt","message":"Invalid request"}}}`,
			wantCode:      errorcode.InvalidArgument,
			wantRetryable: false,
		},
		{
			name:          "authentication",
			event:         `{"type":"error","error":{"type":"authentication_error","message":"Token expired"}}`,
			wantCode:      errorcode.Unauthenticated,
			wantRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var event openAICodexStreamWire
			if err := json.Unmarshal([]byte(tt.event), &event); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			err := openAICodexStreamError(event)
			if got := errorcode.CodeOf(err); got != tt.wantCode {
				t.Errorf("error code = %q, want %q; error = %v", got, tt.wantCode, err)
			}
			if got := model.IsRetryableLLMError(err); got != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v; error = %v", got, tt.wantRetryable, err)
			}
			if got := model.IsBackpressureLLMError(err); got != tt.wantBackpressure {
				t.Errorf("backpressure = %v, want %v; error = %v", got, tt.wantBackpressure, err)
			}
		})
	}
}

func TestXAIResponsesStreamErrorsUseSharedWireButNotCodexPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		event         string
		wantCode      errorcode.Code
		wantRetryable bool
	}{
		{
			name:          "invalid request is terminal",
			event:         `{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid field"}}}`,
			wantCode:      errorcode.InvalidArgument,
			wantRetryable: false,
		},
		{
			name:          "unknown failure is retryable",
			event:         `{"type":"response.failed","response":{"status":"failed","error":null}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
		{
			name:          "shared usage limit type is terminal",
			event:         `{"type":"error","error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			wantCode:      errorcode.ResourceExhausted,
			wantRetryable: false,
		},
		{
			name:          "Codex policy code is not inherited",
			event:         `{"type":"response.failed","response":{"error":{"code":"cyber_policy","message":"Provider-specific policy"}}}`,
			wantCode:      errorcode.Unavailable,
			wantRetryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var event openAICodexStreamWire
			if err := json.Unmarshal([]byte(tt.event), &event); err != nil {
				t.Fatalf("decode event: %v", err)
			}
			err := xAIResponsesStreamError(event)
			if got := errorcode.CodeOf(err); got != tt.wantCode {
				t.Errorf("error code = %q, want %q; error = %v", got, tt.wantCode, err)
			}
			if got := model.IsRetryableLLMError(err); got != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v; error = %v", got, tt.wantRetryable, err)
			}
		})
	}
}
