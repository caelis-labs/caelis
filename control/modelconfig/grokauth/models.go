package grokauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	xAIModelsURL      = DefaultAPIBaseURL + "/models"
	modelsTimeout     = 5 * time.Second
	maxModelsBodySize = 4 << 20
)

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListModels returns language-model IDs visible to the authenticated Grok
// subscription. Callers treat failure as non-fatal so a temporary catalog
// outage does not prevent use of the maintained fallback.
func (m *Manager) ListModels(ctx context.Context, base *http.Client) ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("grokauth: manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if base == nil {
		base = m.httpClient
	}
	client, err := m.AuthenticatedClient(base)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, xAIModelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("grokauth: build models request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("grokauth: fetch models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("grokauth: fetch models failed with status %d", response.StatusCode)
	}
	var catalog modelsResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxModelsBodySize)).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("grokauth: decode models response: %w", err)
	}
	models := make([]string, 0, len(catalog.Data))
	seen := map[string]struct{}{}
	for _, entry := range catalog.Data {
		name := strings.ToLower(strings.TrimSpace(entry.ID))
		if !isLanguageModel(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	sort.Strings(models)
	return models, nil
}

func isLanguageModel(name string) bool {
	if !strings.HasPrefix(name, "grok-") {
		return false
	}
	for _, marker := range []string{"image", "imagine", "video"} {
		if strings.Contains(name, marker) {
			return false
		}
	}
	return true
}
