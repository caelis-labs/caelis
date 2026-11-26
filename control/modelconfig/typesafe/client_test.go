package typesafe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
)

func TestClientValidatesResponsesAndDoesNotExposeErrorBodies(t *testing.T) {
	request := judgment.Request{State: map[string]string{"query": "fixture"}, Questions: map[string]judgment.Question{"route": {Type: judgment.Choice, Instructions: "Choose a route", Criteria: map[string]string{"yes": "match", "no": "no match"}}}}
	for _, tc := range []struct {
		name, body string
		status     int
		valid      bool
	}{
		{"valid", `{"model":"jev-1.13.0","answers":{"route":{"type":"choice","choice":"yes","confidence":1,"probabilities":{"yes":1,"no":0}}},"usage":{"input_tokens":30,"output_tokens":5}}`, 200, true},
		{"rounded down", `{"model":"jev","answers":{"route":{"type":"choice","choice":"yes","confidence":0.8,"probabilities":{"yes":0.90,"no":0.09}}}}`, 200, true},
		{"rounded up", `{"model":"jev","answers":{"route":{"type":"choice","choice":"yes","confidence":0.8,"probabilities":{"yes":0.92,"no":0.09}}}}`, 200, true},
		{"beyond rounding tolerance", `{"model":"jev","answers":{"route":{"type":"choice","choice":"yes","confidence":0.8,"probabilities":{"yes":0.90,"no":0.08}}}}`, 200, false},
		{"unknown choice", `{"model":"jev","answers":{"route":{"type":"choice","choice":"injected","confidence":1,"probabilities":{"yes":1,"no":0}}}}`, 200, false},
		{"choice mismatch", `{"model":"jev","answers":{"route":{"type":"choice","choice":"yes","confidence":1,"probabilities":{"yes":0,"no":1}}}}`, 200, false},
		{"missing answer", `{"model":"jev","answers":{}}`, 200, false},
		{"wrong kind", `{"model":"jev","answers":{"route":{"type":"noul","noul":1}}}}`, 200, false},
		{"invalid distribution", `{"model":"jev","answers":{"route":{"type":"choice","choice":"yes","confidence":1,"probabilities":{"yes":1,"no":1}}}}`, 200, false},
		{"malformed", `private-secret`, 200, false},
		{"unauthorized", `private-secret`, 401, false},
		{"rate limited", `private-secret`, 429, false},
		{"overload", `private-secret`, 529, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/systemone" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer private-secret" {
					t.Error("invalid request route or authentication")
				}
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "private-secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Evaluate(t.Context(), request)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("provider body leaked")
			}
		})
	}
}

func TestClientNeverFollowsRedirectAndHonorsCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, APIKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	request := judgment.Request{Questions: map[string]judgment.Question{"q": {Type: judgment.Noul, Instructions: "Is this a test?"}}}
	_, err = client.Evaluate(t.Context(), request)
	var serviceErr *HTTPError
	if !errors.As(err, &serviceErr) || serviceErr.StatusCode != 307 || calls != 1 {
		t.Fatalf("redirect: calls=%d error=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Evaluate(ctx, request)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancelled call: calls=%d error=%v", calls, err)
	}
}
