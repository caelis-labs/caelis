package providers

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

type providerErrorPayload struct {
	Code    json.RawMessage   `json:"code"`
	Type    string            `json:"type"`
	Status  string            `json:"status"`
	Details []json.RawMessage `json:"details"`
}

type googleErrorDetail struct {
	Type       string `json:"@type"`
	Reason     string `json:"reason"`
	Domain     string `json:"domain"`
	RetryDelay string `json:"retryDelay"`
}

// Google RPC details define a domain-scoped cause and a minimum retry delay.
// QuotaFailure's quota ID/description alone supplies neither a billing decision
// nor a reset time. Keep normal cancellable recovery when that is all we know.
func googleErrorDetails(details []json.RawMessage) (terminal errorcode.Code, after time.Duration) {
	for _, raw := range details {
		var detail googleErrorDetail
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		switch detail.Type {
		case "type.googleapis.com/google.rpc.ErrorInfo":
			if detail.Domain == "googleapis.com" {
				switch detail.Reason {
				case "BILLING_DISABLED", "SERVICE_DISABLED":
					terminal = errorcode.FailedPrecondition
				case "API_KEY_INVALID":
					terminal = errorcode.Unauthenticated
				case "ACCOUNT_STATE_INVALID", "GCP_SUSPENDED":
					terminal = errorcode.PermissionDenied
				}
			}
		case "type.googleapis.com/google.rpc.RetryInfo":
			// Protobuf Duration JSON is seconds with an optional fractional part.
			// Exclude Go duration extensions (hours, minutes, chained units).
			raw := strings.TrimSuffix(detail.RetryDelay, "s")
			if raw == detail.RetryDelay || raw == "" || strings.Trim(raw, "0123456789.") != "" {
				continue
			}
			if delay, err := time.ParseDuration(detail.RetryDelay); err == nil && delay > 0 {
				after = max(after, delay)
			}
		}
	}
	return
}

func googleStatusCode(status string) (errorcode.Code, bool) {
	switch status {
	case "RESOURCE_EXHAUSTED":
		return errorcode.RateLimited, true
	case "UNAVAILABLE", "INTERNAL":
		return errorcode.Unavailable, true
	case "DEADLINE_EXCEEDED":
		return errorcode.Timeout, true
	case "CANCELLED":
		return errorcode.Cancelled, true
	case "UNAUTHENTICATED":
		return errorcode.Unauthenticated, true
	case "PERMISSION_DENIED":
		return errorcode.PermissionDenied, true
	case "INVALID_ARGUMENT", "OUT_OF_RANGE":
		return errorcode.InvalidArgument, true
	case "FAILED_PRECONDITION":
		return errorcode.FailedPrecondition, true
	case "NOT_FOUND":
		return errorcode.NotFound, true
	case "UNIMPLEMENTED":
		return errorcode.Unsupported, true
	default:
		return "", false
	}
}
