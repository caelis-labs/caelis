package gatewayapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
)

func TestModelServiceRoutesGrokOAuthAndAccountModels(t *testing.T) {
	client := &http.Client{Transport: grokOAuthHandlerTransport{handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		switch {
		case request.URL.Host == "auth.x.ai" && request.URL.Path == "/oauth2/device/code":
			writeGatewayJSON(t, writer, map[string]any{
				"device_code": "device", "user_code": "ABCD",
				"verification_uri": "https://auth.x.ai/activate", "expires_in": 60, "interval": 1,
			})
		case request.URL.Host == "auth.x.ai" && request.URL.Path == "/oauth2/token":
			writeGatewayJSON(t, writer, map[string]any{
				"access_token": "access", "refresh_token": "refresh", "expires_in": 3600,
			})
		case request.URL.Host == "cli-chat-proxy.grok.com" && request.URL.Path == "/v1/models":
			if request.Header.Get("Authorization") != "Bearer access" {
				t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
			}
			writeGatewayJSON(t, writer, map[string]any{"data": []any{
				map[string]any{"id": "grok-4.5"},
				map[string]any{"id": "grok-imagine-image"},
				map[string]any{"id": "grok-4.20"},
			}})
		default:
			http.NotFound(writer, request)
		}
	})}}
	manager, err := grokauth.NewManager(grokauth.Options{
		HTTPClient: client, Issuer: "https://auth.x.ai", CredentialPath: grokauth.DefaultCredentialPath(t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := ModelService{stack: &Stack{grokAuth: manager}}
	result, err := service.Authenticate(context.Background(), modelconfig.AuthenticateRequest{
		Provider: "grok", HTTPClient: client, Purpose: modelconfig.AuthPurposeModelSelection,
	})
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if !result.ModelCatalogAuthoritative || strings.Join(result.SelectableModels, ",") != "grok-4.20,grok-4.5" {
		t.Fatalf("Authenticate() result = %#v", result)
	}
}

func TestPrepareProviderCredentialsRejectsGrokOAuthAPIKey(t *testing.T) {
	stack := &Stack{}
	_, _, err := stack.prepareProviderCredentials([]ModelConfig{{
		Provider: "xai", API: model.APIXAIResponses, Model: "grok-4.5",
		BaseURL: modelconfig.GrokOAuthBaseURL, AuthType: model.AuthOAuthToken,
		CredentialRef: modelconfig.GrokOAuthCredentialRef, Token: "must-not-persist",
	}})
	if err == nil || !strings.Contains(err.Error(), "must not carry an API key") {
		t.Fatalf("prepareProviderCredentials() error = %v", err)
	}
}

func TestGrok45AdvertisesImagePromptCapability(t *testing.T) {
	if !modelConfigSupportsImages(ModelConfig{Provider: "xai", Model: "grok-4.5"}) {
		t.Fatal("modelConfigSupportsImages(xai/grok-4.5) = false, want true")
	}
}

type grokOAuthHandlerTransport struct {
	handler http.Handler
}

func (t grokOAuthHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	serverRequest := request.Clone(request.Context())
	serverRequest.Header = request.Header.Clone()
	serverRequest.Host = request.URL.Host
	serverRequest.RequestURI = request.URL.RequestURI()
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, serverRequest)
	response := recorder.Result()
	response.Request = request
	return response, nil
}

func writeGatewayJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode JSON: %v", err)
	}
}
