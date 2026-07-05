package web

import (
	"context"
	"net/http"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/argparse"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

const FetchToolName = "web_fetch"

const (
	defaultFetchTimeout     = 30 * time.Second
	maxFetchTimeout         = 120 * time.Second
	defaultMaxResponseBytes = 5 * 1024 * 1024
	defaultArtifactRootName = "caelis-web-fetch"
	defaultArtifactMaxAge   = 24 * time.Hour
	defaultArtifactMaxFiles = 128
	defaultArtifactMaxBytes = 64 * 1024 * 1024
)

type FetchConfig struct {
	Client              *http.Client
	AllowPrivateNetwork bool
	MaxResponseBytes    int64
	ArtifactDir         string
	ArtifactMaxAge      time.Duration
	ArtifactMaxFiles    int
	ArtifactMaxBytes    int64
}

type FetchTool struct {
	cfg    FetchConfig
	client *http.Client
}

func NewFetch(cfg FetchConfig) (*FetchTool, error) {
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	if cfg.ArtifactMaxAge <= 0 {
		cfg.ArtifactMaxAge = defaultArtifactMaxAge
	}
	if cfg.ArtifactMaxFiles <= 0 {
		cfg.ArtifactMaxFiles = defaultArtifactMaxFiles
	}
	if cfg.ArtifactMaxBytes <= 0 {
		cfg.ArtifactMaxBytes = defaultArtifactMaxBytes
	}
	client := fetchHTTPClient(cfg)
	return &FetchTool{cfg: cfg, client: client}, nil
}

func (t *FetchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        FetchToolName,
		Description: "Fetch and read one specific http or https URL. Use this after the user provides a URL or after web_search returns a result that needs source inspection. This tool does not search, follow arbitrary browsing tasks, or discover related pages. It returns cleaned markdown by default, can return text or raw html when requested, and includes an artifact_path for recalling the original fetched content if global tool-result truncation hides details.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"minLength":   1,
					"description": "Exact fully-qualified http or https URL to retrieve. Do not pass a search query here; use web_search first if you need discovery.",
				},
				"format": map[string]any{
					"type":        "string",
					"enum":        []string{"markdown", "text", "html"},
					"description": "Return format. Use markdown for readable pages, text for plain extracted text, and html only when raw markup is needed. Defaults to markdown.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     int(maxFetchTimeout.Seconds()),
					"description": "Optional timeout in seconds for DNS, redirects, and content download. Defaults to 30 and is capped at 120.",
				},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(true, false, false, true),
	}
}

func (t *FetchTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := toolutil.WithContextCancel(ctx); err != nil {
		return tool.Result{}, err
	}
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := tool.RejectUnknownArgs(args, "url", "format", "timeout"); err != nil {
		return tool.Result{}, err
	}
	rawURL, err := argparse.String(args, "url", true)
	if err != nil {
		return tool.Result{}, err
	}
	format, err := argparse.String(args, "format", false)
	if err != nil {
		return tool.Result{}, err
	}
	format, err = normalizeFetchFormat(format)
	if err != nil {
		return tool.Result{}, err
	}
	timeoutSeconds, err := argparse.Int(args, "timeout", int(defaultFetchTimeout.Seconds()))
	if err != nil {
		return tool.Result{}, err
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	if timeout > maxFetchTimeout {
		timeout = maxFetchTimeout
	}
	u, err := validateFetchURL(rawURL)
	if err != nil {
		return tool.Result{}, err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !t.cfg.AllowPrivateNetwork {
		if err := rejectPrivateFetchTarget(fetchCtx, u); err != nil {
			return tool.Result{}, err
		}
	}

	resp, body, err := t.fetch(fetchCtx, u, format)
	if err != nil {
		return tool.Result{}, err
	}
	payload := t.renderResponse(resp, body, format)
	return toolutil.JSONResult(FetchToolName, payload, map[string]any{
		"url":       u.String(),
		"final_url": resp.finalURL,
		"format":    format,
	})
}

var _ tool.Tool = (*FetchTool)(nil)
