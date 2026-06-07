package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestCoalesceHTTPClientUsesLongRequestFriendlyTransport(t *testing.T) {
	client := coalesceHTTPClient(nil)
	if client == nil {
		t.Fatal("coalesceHTTPClient(nil) = nil")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("ResponseHeaderTimeout = %s, want no default header deadline", transport.ResponseHeaderTimeout)
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Fatal("TLSHandshakeTimeout must be set")
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatal("IdleConnTimeout must be set")
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext must be set")
	}
}

func TestOpenAIProviderDefaultsBaseURLAndAcceptsHTTPClient(t *testing.T) {
	custom := &http.Client{}
	p := NewOpenAI(OpenAIConfig{Model: "gpt-test", HTTPClient: custom})
	if p.baseURL != defaultOpenAIBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultOpenAIBaseURL)
	}
	if p.client != custom {
		t.Fatal("OpenAI provider did not retain configured HTTPClient")
	}
}

func TestOpenAIProviderUsesConfiguredHTTPClientAndNormalizedBaseURL(t *testing.T) {
	rt := &recordingRoundTripper{}
	p := NewOpenAI(OpenAIConfig{
		BaseURL:    "https://provider.test/v1/",
		Model:      "gpt-test",
		HTTPClient: &http.Client{Transport: rt},
	})

	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if rt.url != "https://provider.test/v1/chat/completions" {
		t.Fatalf("request URL = %q, want normalized chat completions endpoint", rt.url)
	}
}

type recordingRoundTripper struct {
	url string
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.url = req.URL.String()
	body := io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\ndata: [DONE]\n\n"))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       body,
		Request:    req,
	}, nil
}
