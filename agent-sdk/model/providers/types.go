package providers

import (
	"net/http"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// AuthConfig is provider-agnostic auth configuration.
type AuthConfig struct {
	Type      model.AuthType
	Token     string
	HeaderKey string
	Prefix    string
}

// Config defines the resolved endpoint and model settings for one provider.
type Config struct {
	Provider   string
	API        model.APIType
	Model      string
	BaseURL    string
	Headers    map[string]string
	HTTPClient *http.Client
	Timeout    time.Duration
	// UserAgent is the caller's product identity for model discovery and
	// direct HTTP adapters. SDK-managed generation uses its library's identity.
	UserAgent string
	// StreamResponseHeaderTimeout bounds the wait for an HTTP streaming
	// response to return headers. It does not bound the lifetime of the body.
	StreamResponseHeaderTimeout time.Duration
	StreamFirstEventTimeout     time.Duration
	// StreamIdleTimeout bounds silence between complete SSE data events after
	// the stream starts.
	StreamIdleTimeout   time.Duration
	MaxOutputTok        int
	ContextWindowTokens int
	ImageInput          bool
	ReasoningLevels     []string
	ReasoningMode       string

	Auth  AuthConfig
	Retry model.RetryConfig
}
