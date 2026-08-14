package providers

import (
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type responsesStreamErrorDetails struct {
	errorType string
	code      string
	message   string
}

type responsesErrorClassification struct {
	errorCode    errorcode.Code
	retryable    bool
	backpressure bool
}

type responsesProviderError struct {
	provider string
	details  responsesStreamErrorDetails
	class    responsesErrorClassification
}

var responsesTerminalErrorTypes = map[string]responsesErrorClassification{
	"authentication_error":  {errorCode: errorcode.Unauthenticated},
	"authorization_error":   {errorCode: errorcode.PermissionDenied},
	"invalid_request_error": {errorCode: errorcode.InvalidArgument},
	"permission_error":      {errorCode: errorcode.PermissionDenied},
	"permissions_error":     {errorCode: errorcode.PermissionDenied},
	"usage_limit_reached":   {errorCode: errorcode.ResourceExhausted},
}

var responsesTerminalErrorCodes = map[string]responsesErrorClassification{
	"account_deactivated":        {errorCode: errorcode.PermissionDenied},
	"billing_hard_limit_reached": {errorCode: errorcode.ResourceExhausted},
	"billing_not_active":         {errorCode: errorcode.ResourceExhausted},
	"forbidden":                  {errorCode: errorcode.PermissionDenied},
	"insufficient_quota":         {errorCode: errorcode.ResourceExhausted},
	"invalid_api_key":            {errorCode: errorcode.Unauthenticated},
	"invalid_token":              {errorCode: errorcode.Unauthenticated},
	"missing_required_parameter": {errorCode: errorcode.InvalidArgument},
	"model_not_found":            {errorCode: errorcode.NotFound},
	"permission_denied":          {errorCode: errorcode.PermissionDenied},
	"permissions_error":          {errorCode: errorcode.PermissionDenied},
	"quota_exceeded":             {errorCode: errorcode.ResourceExhausted},
	"token_expired":              {errorCode: errorcode.Unauthenticated},
	"unauthorized":               {errorCode: errorcode.Unauthenticated},
	"unsupported_value":          {errorCode: errorcode.InvalidArgument},
}

var responsesTransientErrorCodes = map[string]responsesErrorClassification{
	"auth_service_unavailable":           {errorCode: errorcode.Unavailable, retryable: true},
	"connection_error":                   {errorCode: errorcode.Unavailable, retryable: true},
	"overloaded":                         {errorCode: errorcode.Overloaded, retryable: true, backpressure: true},
	"previous_response_not_found":        {errorCode: errorcode.Unavailable, retryable: true},
	"request_timeout":                    {errorCode: errorcode.Timeout, retryable: true},
	"server_error":                       {errorCode: errorcode.Unavailable, retryable: true},
	"server_is_overloaded":               {errorCode: errorcode.Overloaded, retryable: true, backpressure: true},
	"service_unavailable":                {errorCode: errorcode.Unavailable, retryable: true},
	"slow_down":                          {errorCode: errorcode.Overloaded, retryable: true, backpressure: true},
	"too_many_requests":                  {errorCode: errorcode.RateLimited, retryable: true, backpressure: true},
	"upstream_error":                     {errorCode: errorcode.Unavailable, retryable: true},
	"websocket_connection_limit_reached": {errorCode: errorcode.Unavailable, retryable: true},
}

var openAICodexTerminalErrorCodes = map[string]responsesErrorClassification{
	"bio_policy":         {errorCode: errorcode.InvalidArgument},
	"cyber_policy":       {errorCode: errorcode.PermissionDenied},
	"usage_not_included": {errorCode: errorcode.FailedPrecondition},
}

func (e *responsesProviderError) Error() string {
	if e == nil {
		return "responses: provider error"
	}
	provider := strings.TrimSpace(e.provider)
	if provider == "" {
		provider = "responses"
	}
	identifier := strings.TrimSpace(e.details.code)
	if errorType := strings.TrimSpace(e.details.errorType); errorType != "" {
		if identifier == "" {
			identifier = errorType
		} else if !strings.EqualFold(errorType, identifier) {
			identifier = errorType + "/" + identifier
		}
	}
	message := strings.TrimSpace(e.details.message)
	switch {
	case identifier != "" && message != "":
		return provider + ": " + identifier + ": " + message
	case message != "":
		return provider + ": " + message
	case identifier != "":
		return provider + ": " + identifier
	default:
		return provider + ": provider error"
	}
}

func (e *responsesProviderError) Retryable() bool {
	return e != nil && e.class.retryable
}

func (e *responsesProviderError) Backpressure() bool {
	return e != nil && e.class.backpressure
}

func (e *responsesProviderError) ErrorCode() errorcode.Code {
	if e == nil {
		return errorcode.Unknown
	}
	return e.class.errorCode
}

func openAICodexStreamError(event openAICodexStreamWire) error {
	return responsesStreamError("openai codex", event, openAICodexTerminalErrorCodes)
}

func xAIResponsesStreamError(event openAICodexStreamWire) error {
	return responsesStreamError("xai responses", event, nil)
}

func responsesStreamError(
	provider string,
	event openAICodexStreamWire,
	providerTerminalCodes map[string]responsesErrorClassification,
) error {
	details := extractResponsesStreamError(event)
	class := classifyResponsesStreamError(details, providerTerminalCodes)
	providerErr := &responsesProviderError{
		provider: provider,
		details:  details,
		class:    class,
	}
	if strings.EqualFold(details.code, "context_length_exceeded") ||
		looksLikeContextOverflow(details.message, http.StatusBadRequest) {
		return &model.ContextOverflowError{Cause: providerErr}
	}
	return providerErr
}

func extractResponsesStreamError(event openAICodexStreamWire) responsesStreamErrorDetails {
	details := responsesStreamErrorDetails{
		code:    strings.TrimSpace(event.Code),
		message: strings.TrimSpace(event.Message),
	}
	if event.Response != nil {
		details.merge(event.Response.Error)
	}
	details.merge(event.Error)
	return details
}

func (d *responsesStreamErrorDetails) merge(payload *openAICodexErrorPayload) {
	if d == nil || payload == nil {
		return
	}
	if d.errorType == "" {
		d.errorType = strings.TrimSpace(payload.Type)
	}
	if d.code == "" {
		d.code = strings.TrimSpace(payload.Code)
	}
	if d.message == "" {
		d.message = strings.TrimSpace(payload.Message)
	}
}

func classifyResponsesStreamError(
	details responsesStreamErrorDetails,
	providerTerminalCodes map[string]responsesErrorClassification,
) responsesErrorClassification {
	errorType := strings.ToLower(strings.TrimSpace(details.errorType))
	code := strings.ToLower(strings.TrimSpace(details.code))
	// A recognized code is more specific than its broad error type. In
	// particular, Responses may carry transient connection or auth-service
	// failures under invalid_request_error or authentication_error.
	if class, ok := responsesTransientErrorCodes[code]; ok {
		return class
	}
	if class, ok := responsesTerminalErrorTypes[errorType]; ok {
		return class
	}
	if class, ok := responsesTerminalErrorCodes[code]; ok {
		return class
	}
	if class, ok := providerTerminalCodes[code]; ok {
		return class
	}
	switch {
	case strings.HasPrefix(code, "invalid_"):
		return responsesErrorClassification{errorCode: errorcode.InvalidArgument}
	case strings.HasPrefix(code, "rate_limit"):
		return responsesErrorClassification{
			errorCode:    errorcode.RateLimited,
			retryable:    true,
			backpressure: true,
		}
	case strings.HasPrefix(code, "overload"):
		return responsesErrorClassification{
			errorCode:    errorcode.Overloaded,
			retryable:    true,
			backpressure: true,
		}
	case code == "timeout" || strings.HasSuffix(code, "_timeout"):
		return responsesErrorClassification{errorCode: errorcode.Timeout, retryable: true}
	default:
		// Responses gateways occasionally emit empty or novel failure envelopes.
		// Unknown failures remain retryable; only explicit permanent types and
		// codes above can override this fail-safe transport policy.
		return responsesErrorClassification{errorCode: errorcode.Unavailable, retryable: true}
	}
}
