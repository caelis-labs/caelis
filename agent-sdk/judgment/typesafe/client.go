// Package typesafe implements TypeSafe's System One evaluation protocol.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/judgment"
)

const (
	DefaultBaseURL = "https://api.typesafe.ai/v1"
	DefaultModel   = "jev-1.13.0"
	maxBodyBytes   = 2 << 20
)

// Config fixes endpoint, credentials, and model for one client. Timeout bounds
// the complete call; the caller's earlier deadline always takes precedence.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client evaluates requests without retaining their evidence or answers.
type Client struct{ config Config }

// New validates configuration without making a network request.
func New(config Config) (*Client, error) {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	endpoint, err := url.Parse(config.BaseURL)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") {
		return nil, fmt.Errorf("typesafe: invalid API endpoint")
	}
	config.APIKey = strings.TrimSpace(config.APIKey)
	if config.APIKey == "" {
		return nil, fmt.Errorf("typesafe: API key is required")
	}
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	// Never forward a credential or request evidence to a redirected endpoint.
	httpClient := *config.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &httpClient
	return &Client{config: config}, nil
}

func (c *Client) Name() string { return c.config.Model }

// ProviderName identifies the provider for invocation accounting.
func (c *Client) ProviderName() string { return "typesafe" }

// HTTPError reports a service failure without exposing provider bodies or
// credentials. RetryAfter is advisory and must fit the caller's total budget.
type HTTPError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("typesafe: HTTP %d", e.StatusCode) }

// Evaluate makes one bounded request. Retry and fallback decisions belong to
// the calling workflow, so an optional ranking call cannot stall that workflow.
func (c *Client) Evaluate(ctx context.Context, request judgment.Request) (judgment.Response, error) {
	if len(request.Questions) == 0 {
		return judgment.Response{}, fmt.Errorf("typesafe: at least one question is required")
	}
	for id, question := range request.Questions {
		if id == "" || question.Instructions == nil || (question.Type != judgment.Choice && question.Type != judgment.Noul && question.Type != judgment.Score) {
			return judgment.Response{}, fmt.Errorf("typesafe: invalid question")
		}
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		judgment.Request
	}{c.config.Model, request})
	if err != nil {
		return judgment.Response{}, fmt.Errorf("typesafe: encode request: %w", err)
	}
	if len(body) > maxBodyBytes {
		return judgment.Response{}, fmt.Errorf("typesafe: request exceeds byte budget")
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+"/systemone", bytes.NewReader(body))
	if err != nil {
		return judgment.Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return judgment.Response{}, ctx.Err()
		}
		return judgment.Response{}, fmt.Errorf("typesafe: transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		retry, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		return judgment.Response{}, &HTTPError{StatusCode: response.StatusCode, RetryAfter: time.Duration(max(0, min(retry, 3600))) * time.Second}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes {
		return judgment.Response{}, fmt.Errorf("typesafe: response unavailable or oversized")
	}
	var result judgment.Response
	if err := json.Unmarshal(raw, &result); err != nil {
		return judgment.Response{}, fmt.Errorf("typesafe: malformed response")
	}
	if err := validateResponse(request, result); err != nil {
		return result, err
	}
	return result, nil
}

func probability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validateResponse(request judgment.Request, result judgment.Response) error {
	if strings.TrimSpace(result.Model) == "" || len(result.Answers) != len(request.Questions) || result.Usage.InputTokens < 0 || result.Usage.OutputTokens < 0 {
		return fmt.Errorf("typesafe: incomplete response")
	}
	for id, question := range request.Questions {
		answer, ok := result.Answers[id]
		if !ok || answer.Type != question.Type {
			return fmt.Errorf("typesafe: missing or mismatched answer")
		}
		if answer.Type == judgment.Noul {
			if answer.Noul == nil || !probability(*answer.Noul) {
				return fmt.Errorf("typesafe: invalid yes/no probability")
			}
			continue
		}
		if answer.Confidence == nil || !probability(*answer.Confidence) || len(answer.Probabilities) == 0 {
			return fmt.Errorf("typesafe: missing answer distribution")
		}
		total := 0.0
		for _, p := range answer.Probabilities {
			if !probability(p) {
				return fmt.Errorf("typesafe: invalid answer probability")
			}
			total += p
		}
		if math.Abs(total-1) > 0.01 {
			return fmt.Errorf("typesafe: invalid probability sum")
		}
		criteria, err := json.Marshal(question.Criteria)
		if err != nil {
			return fmt.Errorf("typesafe: invalid criteria")
		}
		if answer.Type == judgment.Choice {
			var options map[string]json.RawMessage
			if json.Unmarshal(criteria, &options) != nil || len(options) != len(answer.Probabilities) {
				return fmt.Errorf("typesafe: choice distribution does not match options")
			}
			if _, exists := options[answer.Choice]; !exists {
				return fmt.Errorf("typesafe: unknown choice")
			}
			for option := range answer.Probabilities {
				if _, exists := options[option]; !exists {
					return fmt.Errorf("typesafe: unknown choice probability")
				}
				if answer.Probabilities[option] > answer.Probabilities[answer.Choice] {
					return fmt.Errorf("typesafe: choice contradicts its distribution")
				}
			}
		} else {
			var levels []json.RawMessage
			if json.Unmarshal(criteria, &levels) != nil || len(levels) < 2 || len(levels) != len(answer.Probabilities) || answer.Score == nil || *answer.Score < 0 || *answer.Score > float64(len(levels)-1) {
				return fmt.Errorf("typesafe: invalid score")
			}
			for index := range levels {
				if _, exists := answer.Probabilities[strconv.Itoa(index)]; !exists {
					return fmt.Errorf("typesafe: unknown score level")
				}
			}
		}
	}
	return nil
}
