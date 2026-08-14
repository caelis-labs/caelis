package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestServerWebSearchTimeoutContract(t *testing.T) {
	type providerCase struct {
		name  string
		build func(time.Duration, *http.Client) model.LLM
	}
	cases := []providerCase{
		{
			name: "openai-codex",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newOpenAICodex(Config{
					Provider:   "openai-codex",
					Model:      "gpt-test",
					BaseURL:    "https://provider.test",
					HTTPClient: client,
					Timeout:    timeout,
				})
			},
		},
		{
			name: "xai",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newXAIResponses(Config{
					Provider:   "xai",
					Model:      "grok-test",
					BaseURL:    "https://provider.test",
					HTTPClient: client,
					Timeout:    timeout,
				})
			},
		},
		{
			name: "gemini",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newGemini(Config{
					Provider:   "gemini",
					Model:      "gemini-test",
					BaseURL:    "https://provider.test",
					HTTPClient: client,
					Timeout:    timeout,
				}, "token")
			},
		},
		{
			name: "xiaomi",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newMimo(Config{
					Provider:   "xiaomi",
					Model:      "mimo-test",
					BaseURL:    "https://provider.test",
					HTTPClient: client,
					Timeout:    timeout,
				}, "token")
			},
		},
		{
			name: "anthropic",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newAnthropic(Config{
					Provider:   "anthropic",
					Model:      "claude-test",
					BaseURL:    "https://provider.test",
					HTTPClient: client,
					Timeout:    timeout,
				}, "token")
			},
		},
		{
			name: "deepseek",
			build: func(timeout time.Duration, client *http.Client) model.LLM {
				return newDeepSeek(Config{
					Provider:   "deepseek",
					Model:      "deepseek-test",
					BaseURL:    "https://provider.test/anthropic",
					HTTPClient: client,
					Timeout:    timeout,
				}, "token")
			},
		},
	}

	scenarios := []struct {
		name              string
		configuredTimeout time.Duration
		callerTimeout     time.Duration
		wantDeadline      bool
		minRemaining      time.Duration
		maxRemaining      time.Duration
	}{
		{
			name: "caller context has no hidden deadline",
		},
		{
			name:              "explicit provider timeout is honored",
			configuredTimeout: 45 * time.Second,
			wantDeadline:      true,
			minRemaining:      40 * time.Second,
			maxRemaining:      46 * time.Second,
		},
		{
			name:              "earlier caller deadline wins",
			configuredTimeout: 45 * time.Second,
			callerTimeout:     10 * time.Second,
			wantDeadline:      true,
			minRemaining:      8 * time.Second,
			maxRemaining:      11 * time.Second,
		},
	}

	type deadlineObservation struct {
		deadline time.Time
		ok       bool
	}
	for _, provider := range cases {
		for _, scenario := range scenarios {
			t.Run(provider.name+"/"+scenario.name, func(t *testing.T) {
				observed := make(chan deadlineObservation, 1)
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					deadline, ok := req.Context().Deadline()
					select {
					case observed <- deadlineObservation{deadline: deadline, ok: ok}:
					default:
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				})}
				searcher, ok := provider.build(scenario.configuredTimeout, client).(model.WebSearcher)
				if !ok {
					t.Fatalf("%s model does not implement WebSearcher", provider.name)
				}
				var (
					ctx    context.Context
					cancel context.CancelFunc
				)
				if scenario.callerTimeout > 0 {
					ctx, cancel = context.WithTimeout(context.Background(), scenario.callerTimeout)
				} else {
					ctx, cancel = context.WithCancel(context.Background())
				}
				errCh := make(chan error, 1)
				go func() {
					_, err := searcher.SearchWeb(ctx, model.WebSearchRequest{Query: "deadline probe"})
					errCh <- err
				}()

				var got deadlineObservation
				select {
				case got = <-observed:
				case <-time.After(time.Second):
					cancel()
					t.Fatal("SearchWeb() did not issue an HTTP request")
				}
				if got.ok != scenario.wantDeadline {
					cancel()
					t.Fatalf("request context deadline present = %t, want %t", got.ok, scenario.wantDeadline)
				}
				if got.ok {
					remaining := time.Until(got.deadline)
					if remaining < scenario.minRemaining || remaining > scenario.maxRemaining {
						cancel()
						t.Fatalf("request deadline remaining = %s, want within [%s, %s]", remaining, scenario.minRemaining, scenario.maxRemaining)
					}
				}
				cancel()
				select {
				case err := <-errCh:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("SearchWeb() error = %v, want context.Canceled", err)
					}
				case <-time.After(time.Second):
					t.Fatal("SearchWeb() did not return after caller cancellation")
				}
			})
		}
		t.Run(provider.name+"/ordinary streaming ignores request timeout", func(t *testing.T) {
			observed := make(chan deadlineObservation, 1)
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				select {
				case observed <- deadlineObservation{deadline: deadline, ok: ok}:
				default:
				}
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}
			llm := provider.build(45*time.Second, client)
			ctx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				var finalErr error
				for _, err := range llm.Generate(ctx, &model.Request{
					Messages: []model.Message{model.NewTextMessage(model.RoleUser, "stream probe")},
					Stream:   true,
				}) {
					if err != nil {
						finalErr = err
						break
					}
				}
				errCh <- finalErr
			}()

			select {
			case got := <-observed:
				if got.ok {
					cancel()
					t.Fatalf("stream request context has unexpected deadline %s", got.deadline)
				}
			case <-time.After(time.Second):
				cancel()
				t.Fatal("Generate() did not issue an HTTP request")
			}
			cancel()
			select {
			case err := <-errCh:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Generate() error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Generate() did not return after caller cancellation")
			}
		})
	}
}
