package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultModelsDevURL = "https://models.dev/api.json"

type capEntry struct {
	ContextWindow          int      `json:"context_window,omitempty"`
	MaxOutput              int      `json:"max_output,omitempty"`
	DefaultMaxOutput       int      `json:"default_max_output,omitempty"`
	ToolCalls              bool     `json:"tool_calls,omitempty"`
	Reasoning              bool     `json:"reasoning,omitempty"`
	ReasoningMode          string   `json:"reasoning_mode,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	Images                 bool     `json:"images,omitempty"`
	JSONOutput             bool     `json:"json_output,omitempty"`
}

type capSnapshot map[string]capEntry

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string              `json:"id"`
	Reasoning        bool                `json:"reasoning"`
	ToolCall         bool                `json:"tool_call"`
	Attachment       bool                `json:"attachment"`
	StructuredOutput bool                `json:"structured_output"`
	Limit            modelsDevModelLimit `json:"limit"`
}

type modelsDevModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

var providerAlias = map[string]string{
	"google": "gemini",
}

func main() {
	input := flag.String("input", "", "path to a full models.dev api.json snapshot; if empty, fetch -url")
	output := flag.String("output", "control/modelcatalog/models_dev_snapshot.compact.json.gz", "path for the compact gzip snapshot")
	sourceURL := flag.String("url", defaultModelsDevURL, "models.dev API URL used when -input is empty")
	flag.Parse()

	raw, err := readSource(*input, *sourceURL)
	if err != nil {
		fatal(err)
	}
	snap, err := parseModelsDevJSON(raw)
	if err != nil {
		fatal(err)
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		fatal(err)
	}
	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		fatal(err)
	}
	if _, err := zw.Write(encoded); err != nil {
		fatal(err)
	}
	if err := zw.Close(); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*output, compressed.Bytes(), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d models, %d bytes json, %d bytes gzip\n", *output, len(snap), len(encoded), compressed.Len())
}

func readSource(input string, sourceURL string) ([]byte, error) {
	if strings.TrimSpace(input) != "" {
		return os.ReadFile(input)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "caelis/model-cataloggen")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", sourceURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
}

func parseModelsDevJSON(data []byte) (capSnapshot, error) {
	var raw map[string]modelsDevProvider
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	snap := make(capSnapshot, len(raw)*8)
	providerIDs := make([]string, 0, len(raw))
	for providerID := range raw {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		prov := raw[providerID]
		internalProvider := providerID
		if alias, ok := providerAlias[providerID]; ok {
			internalProvider = alias
		}
		modelIDs := make([]string, 0, len(prov.Models))
		for modelID := range prov.Models {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			model := prov.Models[modelID]
			if model.Limit.Context <= 0 && model.Limit.Output <= 0 {
				continue
			}
			entry := capEntry{
				ContextWindow: model.Limit.Context,
				MaxOutput:     model.Limit.Output,
				ToolCalls:     model.ToolCall,
				Reasoning:     model.Reasoning,
				Images:        model.Attachment,
				JSONOutput:    model.StructuredOutput,
			}
			insertCapEntry(snap, internalProvider+":"+strings.ToLower(modelID), entry)
			if derivedProvider, derivedModel, ok := splitVendorModelID(modelID); ok {
				insertCapEntry(snap, derivedProvider+":"+derivedModel, entry)
			}
		}
	}
	return snap, nil
}

func insertCapEntry(snap capSnapshot, key string, entry capEntry) {
	if strings.TrimSpace(key) == "" {
		return
	}
	key = strings.ToLower(key)
	if existing, ok := snap[key]; ok {
		snap[key] = mergeCapEntry(existing, entry)
		return
	}
	snap[key] = entry
}

func mergeCapEntry(existing, next capEntry) capEntry {
	if next.ContextWindow > existing.ContextWindow {
		existing.ContextWindow = next.ContextWindow
	}
	if next.MaxOutput > existing.MaxOutput {
		existing.MaxOutput = next.MaxOutput
	}
	existing.ToolCalls = existing.ToolCalls || next.ToolCalls
	existing.Reasoning = existing.Reasoning || next.Reasoning
	existing.Images = existing.Images || next.Images
	existing.JSONOutput = existing.JSONOutput || next.JSONOutput
	return existing
}

func splitVendorModelID(modelID string) (provider string, model string, ok bool) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(modelID)), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
