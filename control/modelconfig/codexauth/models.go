package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	codexModelsURL     = "https://chatgpt.com/backend-api/codex/models"
	codexModelsTimeout = 5 * time.Second
	maxModelsBodyBytes = 4 << 20
	// The account catalog is version-gated with Codex client compatibility,
	// not the Caelis product version. Keep this aligned with the upstream
	// catalog snapshot used by control/modelconfig.
	codexModelsClientVersion = "0.144.4"
)

type modelsResponse struct {
	Models []remoteModel `json:"models"`
}

type remoteModel struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
	Priority   int    `json:"priority"`
}

// ListModels reads the account-scoped Codex model directory. ChatGPT OAuth
// catalogs are already entitlement-aware; visible entries are not filtered by
// supported_in_api because the official client applies that flag only to
// non-ChatGPT authentication modes.
func (m *Manager) ListModels(ctx context.Context, base *http.Client) ([]string, error) {
	if m == nil {
		return nil, fmt.Errorf("codexauth: manager is nil")
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
	requestURL, err := url.Parse(codexModelsURL)
	if err != nil {
		return nil, fmt.Errorf("codexauth: parse models endpoint: %w", err)
	}
	query := requestURL.Query()
	query.Set("client_version", codexModelsClientVersion)
	requestURL.RawQuery = query.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, codexModelsTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("codexauth: build models request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("originator", "caelis")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("codexauth: fetch models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("codexauth: fetch models failed with status %d", response.StatusCode)
	}
	var catalog modelsResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxModelsBodyBytes))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("codexauth: decode models response: %w", err)
	}
	sort.SliceStable(catalog.Models, func(i, j int) bool {
		if catalog.Models[i].Priority != catalog.Models[j].Priority {
			return catalog.Models[i].Priority < catalog.Models[j].Priority
		}
		return strings.ToLower(catalog.Models[i].Slug) < strings.ToLower(catalog.Models[j].Slug)
	})
	models := make([]string, 0, len(catalog.Models))
	seen := map[string]struct{}{}
	for _, entry := range catalog.Models {
		if !strings.EqualFold(strings.TrimSpace(entry.Visibility), "list") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(entry.Slug))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	return models, nil
}
