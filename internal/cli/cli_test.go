package cli

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

func TestResolveInputFromPrompt(t *testing.T) {
	got, single, err := resolveInput("hello", strings.NewReader(""), true)
	if err != nil {
		t.Fatalf("resolveInput() error = %v", err)
	}
	if !single || got != "hello" {
		t.Fatalf("resolveInput() = %q, %v", got, single)
	}
}

func TestResolveTurnInputForceInteractiveDoesNotConsumePipe(t *testing.T) {
	stdin := strings.NewReader("piped prompt")
	got, single, err := resolveTurnInput("", stdin, false, true)
	if err != nil {
		t.Fatalf("resolveTurnInput() error = %v", err)
	}
	if single || got != "" {
		t.Fatalf("resolveTurnInput() = %q, %v", got, single)
	}

	remaining, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatalf("ReadAll(stdin) error = %v", err)
	}
	if string(remaining) != "piped prompt" {
		t.Fatalf("remaining stdin = %q", remaining)
	}
}

func TestReaderIsTTYUsesInjectedReader(t *testing.T) {
	if readerIsTTY(strings.NewReader("prompt")) {
		t.Fatal("readerIsTTY(strings.Reader) = true, want false for injected non-file stdin")
	}
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer file.Close()
	if readerIsTTY(file) {
		t.Fatal("readerIsTTY(temp file) = true, want false for regular file")
	}
}

func TestParseOutputFormat(t *testing.T) {
	if got, err := parseOutputFormat("json"); err != nil || got != outputJSON {
		t.Fatalf("parseOutputFormat() = %q, %v", got, err)
	}
	if _, err := parseOutputFormat("xml"); err == nil {
		t.Fatal("parseOutputFormat(xml) error = nil")
	}
}

func TestRunHelpReturnsNil(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(context.Background(), []string{"-h"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run(-h) error = %v, want nil", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Usage of caelis:") ||
		!strings.Contains(got, "Permission mode: auto-review|manual") {
		t.Fatalf("stderr = %q, want help usage with permission mode", got)
	}
}

func TestDefaultStoreDirUsesHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	want := filepath.Join(home, ".caelis")
	if got := defaultStoreDir(t.TempDir()); got != want {
		t.Fatalf("defaultStoreDir() = %q, want %q", got, want)
	}
}

func TestPreferredSessionIDDefaultsDifferBetweenInteractiveAndHeadless(t *testing.T) {
	if got := preferredInteractiveSessionID(""); got != "" {
		t.Fatalf("preferredInteractiveSessionID(\"\") = %q, want empty for fresh TUI session", got)
	}
	if got := preferredHeadlessSessionID(""); got != "" {
		t.Fatalf("preferredHeadlessSessionID(\"\") = %q, want empty for fresh headless session", got)
	}
	if got := preferredInteractiveSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredInteractiveSessionID(\"sticky\") = %q, want sticky", got)
	}
	if got := preferredHeadlessSessionID("sticky"); got != "sticky" {
		t.Fatalf("preferredHeadlessSessionID(\"sticky\") = %q, want sticky", got)
	}
}

func cliTestStoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	data := []byte(`{"sandbox":{"requested_type":"host"}}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatalf("write CLI test config: %v", err)
	}
	return dir
}

func TestStreamHandleWritesAssistantTextAndDeniesApproval(t *testing.T) {
	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Event: kernel.Event{
				Kind:            kernel.EventKindApprovalRequested,
				ApprovalPayload: &kernel.ApprovalPayload{Status: kernel.ApprovalStatusPending},
			},
		},
		{
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Text:  "interactive ok",
					Final: true,
				},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "denied by default") {
		t.Fatalf("stderr = %q", got)
	}
	if len(handle.submits) != 1 || handle.submits[0].Approval == nil || handle.submits[0].Approval.Approved {
		t.Fatalf("submits = %#v", handle.submits)
	}
}

func TestStreamHandleIgnoresAutomaticApprovalReviewEvents(t *testing.T) {
	handle := newFakeHandle([]kernel.EventEnvelope{
		{
			Event: kernel.Event{
				Kind: kernel.EventKindApprovalReview,
				ApprovalPayload: &kernel.ApprovalPayload{
					Status:         kernel.ApprovalStatusPending,
					ReviewStatus:   kernel.ApprovalReviewStatusInProgress,
					DecisionSource: "auto-review",
				},
			},
		},
		{
			Event: kernel.Event{
				Kind: kernel.EventKindAssistantMessage,
				Narrative: &kernel.NarrativePayload{
					Role:  kernel.NarrativeRoleAssistant,
					Text:  "interactive ok",
					Final: true,
				},
			},
		},
	})
	var out bytes.Buffer
	var errBuf bytes.Buffer
	if err := streamHandle(context.Background(), handle, &out, &errBuf); err != nil {
		t.Fatalf("streamHandle() error = %v", err)
	}
	if got := out.String(); !strings.Contains(got, "interactive ok") {
		t.Fatalf("stdout = %q", got)
	}
	if got := errBuf.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
	if len(handle.submits) != 0 {
		t.Fatalf("submits = %#v, want no manual decision for auto-review event", handle.submits)
	}
}

func TestRunDoctorJSONDoesNotLeakToken(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-doctor",
		"-format", "json",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "minimax",
		"-model", "MiniMax-M1",
		"-token", "super-secret-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(-doctor) error = %v", err)
	}
	if strings.Contains(out.String(), "super-secret-token") {
		t.Fatalf("doctor output leaked token: %q", out.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("doctor json decode error = %v", err)
	}
	if got := report["active_provider"]; got != "minimax" {
		t.Fatalf("active_provider = %#v, want minimax", got)
	}
}

func TestRunACPSubcommandConstructsStdioServer(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := run(ctx, []string{
		"acp",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "acp-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "ollama",
		"-model", "llama3",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(acp) error = %v; stderr=%q", err, errBuf.String())
	}
}

func TestRunHeadlessUsesCoreLocalStack(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-test",
			"choices":[{"message":{"role":"assistant","content":"core pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "openai",
		"-model", "gpt-test",
		"-base-url", server.URL,
		"-auth-type", "none",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "core pong" || result.PromptTokens != 3 || strings.TrimSpace(result.SessionID) == "" {
		t.Fatalf("headless result = %#v, want core output and usage", result)
	}
	if captured.Model != "gpt-test" || len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}
	if !capturedCLITool(captured.Tools, "task") || !capturedCLITool(captured.Tools, "write_file") {
		t.Fatalf("captured tools = %#v, want core builtin tools", captured.Tools)
	}
}

func TestCoreTUIDriverUsesCoreLocalStack(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-tui",
			"choices":[{"message":{"role":"assistant","content":"core tui pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	workspace := t.TempDir()
	cfg, err := normalizeConfig(cliConfig{
		AppName:        "caelis",
		UserID:         "tui-user",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "tui-ws",
		WorkspaceCWD:   workspace,
		PermissionMode: "auto-review",
		Model: cliModelConfig{
			Alias:        "tui-model",
			Provider:     "openai",
			API:          providers.APIOpenAI,
			Model:        "gpt-tui",
			BaseURL:      server.URL,
			AuthType:     providers.AuthNone,
			MaxOutputTok: 4096,
		},
		Sandbox: cliSandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	stack, err := newCoreLocalStack(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newCoreLocalStack() error = %v", err)
	}
	driver, err := newCoreTUIDriver(context.Background(), stack, "", "cli-tui", renderModelText(cfg))
	if err != nil {
		t.Fatalf("newCoreTUIDriver() error = %v", err)
	}

	turn, err := driver.Submit(context.Background(), gatewaydriver.Submission{Text: "ping"})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if turn == nil {
		t.Fatal("Submit() turn = nil, want core turn")
	}
	defer turn.Close()

	var assistantText string
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case env, ok := <-turn.Events():
			if !ok {
				if assistantText != "core tui pong" {
					t.Fatalf("assistant text = %q, want core tui pong", assistantText)
				}
				if captured.Model != "gpt-tui" || len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "user" {
					t.Fatalf("captured request = %#v", captured)
				}
				return
			}
			if env.Err != nil {
				t.Fatalf("turn event error = %v", env.Err)
			}
			if env.Event.Narrative != nil && env.Event.Narrative.Role == kernel.NarrativeRoleAssistant {
				assistantText += env.Event.Narrative.Text
			}
		case <-timer.C:
			turn.Cancel()
			t.Fatal("core TUI turn did not finish")
		}
	}
}

func TestCoreTUIStackAllowsEmptyModelConfiguration(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg, err := normalizeConfig(cliConfig{
		AppName:        "caelis",
		UserID:         "empty-model-user",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "empty-model-ws",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "auto-review",
		Sandbox:        cliSandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	stack, err := newCoreLocalStack(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newCoreLocalStack() error = %v", err)
	}
	driver, err := newCoreTUIDriver(context.Background(), stack, "", "cli-tui", renderModelText(cfg))
	if err != nil {
		t.Fatalf("newCoreTUIDriver() error = %v", err)
	}
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatalf("LightweightStatus() error = %v", err)
	}
	if status.Model != "not configured" {
		t.Fatalf("status model = %q, want not configured", status.Model)
	}
}

func TestCoreLocalStackRegistersAssemblyACPAgent(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	cfg, err := normalizeConfig(cliConfig{
		AppName:      "caelis",
		UserID:       "assembly-agent-user",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "assembly-agent-ws",
		WorkspaceCWD: t.TempDir(),
		Sandbox:      cliSandboxConfig{RequestedType: "host"},
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "self",
				Description: "self ACP agent",
				Command:     "self-acp",
				Args:        []string{"--stdio"},
				Env:         map[string]string{"SELF_TOKEN": "secret"},
				WorkDir:     "/tmp/self-agent",
			}},
		},
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	stack, err := newCoreLocalStack(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newCoreLocalStack() error = %v", err)
	}
	agents, err := stack.Services().Agents().List(context.Background())
	if err != nil {
		t.Fatalf("Agents().List() error = %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "self" || agents[0].Command != "self-acp" || agents[0].Env["SELF_TOKEN"] != "secret" {
		t.Fatalf("agents = %#v, want assembly self ACP agent", agents)
	}
}

func TestRunHeadlessUsesCoreOllamaProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q, want /api/chat", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"llama3",
			"message":{"role":"assistant","content":"ollama pong"},
			"done":true,
			"prompt_eval_count":4,
			"eval_count":2
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-ollama-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "ollama",
		"-model", "llama3",
		"-base-url", server.URL,
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "ollama pong" || result.PromptTokens != 4 {
		t.Fatalf("headless result = %#v, want Ollama output and usage", result)
	}
	if captured.Model != "llama3" || len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}
	if !capturedCLITool(captured.Tools, "task") || !capturedCLITool(captured.Tools, "write_file") {
		t.Fatalf("captured tools = %#v, want core builtin tools", captured.Tools)
	}
}

func TestRunHeadlessUsesCoreAnthropicProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var apiKeyHeader string
	var versionHeader string
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		apiKeyHeader = r.Header.Get("x-api-key")
		versionHeader = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_cli",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"anthropic pong"}],
			"usage":{"input_tokens":5,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-anthropic-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "anthropic",
		"-model", "claude-test",
		"-base-url", server.URL,
		"-token", "anthropic-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "anthropic pong" || result.PromptTokens != 5 {
		t.Fatalf("headless result = %#v, want Anthropic output and usage", result)
	}
	if apiKeyHeader != "anthropic-token" || versionHeader == "" {
		t.Fatalf("headers = api-key:%q version:%q", apiKeyHeader, versionHeader)
	}
	if captured.Model != "claude-test" || len(captured.Messages) == 0 || captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}
	if !capturedAnthropicTool(captured.Tools, "task") || !capturedAnthropicTool(captured.Tools, "write_file") {
		t.Fatalf("captured tools = %#v, want core builtin tools", captured.Tools)
	}
}

func TestRunHeadlessUsesCoreGeminiProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var apiKeyHeader string
	var captured struct {
		Contents []struct {
			Role string `json:"role"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-test:generateContent" {
			t.Fatalf("path = %q, want Gemini generateContent", r.URL.Path)
		}
		apiKeyHeader = r.Header.Get("x-goog-api-key")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"modelVersion":"gemini-test",
			"candidates":[{"content":{"role":"model","parts":[{"text":"gemini pong"}]}}],
			"usageMetadata":{"promptTokenCount":6,"candidatesTokenCount":2,"totalTokenCount":8}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-gemini-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "gemini",
		"-model", "gemini-test",
		"-base-url", server.URL + "/v1beta",
		"-token", "gemini-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "gemini pong" || result.PromptTokens != 6 {
		t.Fatalf("headless result = %#v, want Gemini output and usage", result)
	}
	if apiKeyHeader != "gemini-token" {
		t.Fatalf("x-goog-api-key = %q, want token", apiKeyHeader)
	}
	if len(captured.Contents) == 0 || captured.Contents[len(captured.Contents)-1].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
	}
	if !capturedGeminiTool(captured.Tools, "task") || !capturedGeminiTool(captured.Tools, "write_file") {
		t.Fatalf("captured tools = %#v, want core builtin tools", captured.Tools)
	}
}

func TestRunHeadlessUsesCoreCodeFreeProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	credsPath := writeCodeFreeCredentialsForCLITest(t, "272182", "codefree-api-key")
	t.Setenv("CODEFREE_OAUTH_CREDS_PATH", credsPath)
	var headers http.Header
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Stream bool `json:"stream"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/acbackend/codechat/v1/completions" {
			t.Fatalf("path = %q, want CodeFree completions", r.URL.Path)
		}
		headers = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"GLM-4.7",
			"choices":[{"message":{"role":"assistant","content":"codefree pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-codefree-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "codefree",
		"-model", "GLM-4.7",
		"-base-url", server.URL,
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "codefree pong" || result.PromptTokens != 7 {
		t.Fatalf("headless result = %#v, want CodeFree output and usage", result)
	}
	if headers.Get("Authorization") != "Bearer codefree" || headers.Get("Userid") != "272182" ||
		headers.Get("Apikey") != "codefree-api-key" {
		t.Fatalf("headers = auth:%q user:%q apikey:%q", headers.Get("Authorization"), headers.Get("Userid"), headers.Get("Apikey"))
	}
	if captured.Model != "GLM-4.7" || captured.Stream {
		t.Fatalf("captured model/stream = %q/%v", captured.Model, captured.Stream)
	}
	if !capturedCLITool(captured.Tools, "task") || !capturedCLITool(captured.Tools, "write_file") {
		t.Fatalf("captured tools = %#v, want core builtin tools", captured.Tools)
	}
}

func TestRunHeadlessUsesCoreDeepSeekProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var authHeader string
	var captured struct {
		Model           string `json:"model"`
		MaxTokens       int    `json:"max_tokens"`
		ReasoningEffort string `json:"reasoning_effort"`
		Thinking        struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"deepseek-v4-pro",
			"choices":[{"message":{"role":"assistant","content":"deepseek pong","reasoning_content":"thinking"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-deepseek-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "deepseek",
		"-model", "deepseek-v4-pro",
		"-base-url", server.URL,
		"-token", "deepseek-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "deepseek pong" || result.PromptTokens != 6 {
		t.Fatalf("headless result = %#v, want DeepSeek output and usage", result)
	}
	if authHeader != "Bearer deepseek-token" {
		t.Fatalf("authorization = %q, want bearer token", authHeader)
	}
	if captured.Model != "deepseek-v4-pro" || captured.Thinking.Type != "enabled" || captured.ReasoningEffort != "high" || captured.MaxTokens != 32768 {
		t.Fatalf("captured request = %#v, want DeepSeek reasoning defaults", captured)
	}
}

func TestRunHeadlessUsesCoreOpenRouterProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var authHeader string
	var refererHeader string
	var titleHeader string
	var captured struct {
		Model string `json:"model"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		refererHeader = r.Header.Get("HTTP-Referer")
		titleHeader = r.Header.Get("X-Title")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"openai/gpt-4.1",
			"choices":[{"message":{"role":"assistant","content":"openrouter pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-openrouter-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "openrouter",
		"-model", "openrouter/openai/gpt-4.1",
		"-base-url", server.URL,
		"-token", "openrouter-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "openrouter pong" || result.PromptTokens != 7 {
		t.Fatalf("headless result = %#v, want OpenRouter output and usage", result)
	}
	if authHeader != "Bearer openrouter-token" || refererHeader == "" || titleHeader != "Caelis" {
		t.Fatalf("headers = auth:%q referer:%q title:%q", authHeader, refererHeader, titleHeader)
	}
	if captured.Model != "openai/gpt-4.1" {
		t.Fatalf("captured model = %q, want normalized openai/gpt-4.1", captured.Model)
	}
}

func TestRunHeadlessUsesCoreMimoProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var authHeader string
	var captured struct {
		Model string `json:"model"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"mimo-v2-pro",
			"choices":[{"message":{"role":"assistant","content":"mimo pong","reasoning_content":"thinking"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-mimo-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "xiaomi",
		"-model", "mimo-v2-pro",
		"-base-url", server.URL,
		"-token", "mimo-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "mimo pong" || result.PromptTokens != 8 {
		t.Fatalf("headless result = %#v, want Mimo output and usage", result)
	}
	if authHeader != "Bearer mimo-token" || captured.Model != "mimo-v2-pro" {
		t.Fatalf("captured auth/model = %q/%q", authHeader, captured.Model)
	}
}

func TestRunHeadlessUsesCoreVolcengineCodingProvider(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var captured struct {
		Model    string `json:"model"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"doubao-seed-2.0-pro",
			"choices":[{"message":{"role":"assistant","content":"volcengine pong"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}
		}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"-p", "ping",
		"-format", "json",
		"-store-dir", t.TempDir(),
		"-workspace-key", "headless-volcengine-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "volcengine-coding-plan",
		"-model", "doubao-seed-2.0-pro",
		"-base-url", server.URL,
		"-token", "volc-token",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run headless error = %v; stderr=%q", err, errBuf.String())
	}
	var result runResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode headless json: %v; output=%q", err, out.String())
	}
	if result.Output != "volcengine pong" || result.PromptTokens != 9 {
		t.Fatalf("headless result = %#v, want Volcengine output and usage", result)
	}
	if captured.Model != "doubao-seed-2.0-pro" || captured.Thinking.Type != "auto" {
		t.Fatalf("captured request = %#v, want Volcengine auto thinking", captured)
	}
}

func TestRunDoctorSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"doctor",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "doctor-ws",
		"-workspace-cwd", t.TempDir(),
		"-provider", "deepseek",
		"-api", "deepseek",
		"-model", "deepseek-v4-pro",
		"-token-env", "CAELIS_TEST_DOCTOR_TOKEN",
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(doctor) error = %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "active_provider: deepseek") {
		t.Fatalf("doctor text = %q, want active provider line", text)
	}
	if strings.Contains(text, "super-secret") {
		t.Fatalf("doctor text leaked secret: %q", text)
	}
}

func TestRunSandboxSetupSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup) error = %v; stderr=%q", err, errBuf.String())
	}
	text := out.String()
	for _, want := range []string{
		"sandbox_requested_backend: host",
		"sandbox_resolved_backend: host",
		"sandbox_route: host",
		"sandbox_setup_required: false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sandbox setup text = %q, want %q", text, want)
		}
	}
}

func TestRunSandboxSetupSubcommandJSONOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-format", "json",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup json) error = %v; stderr=%q", err, errBuf.String())
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("sandbox setup json decode error = %v", err)
	}
	if got := report["ResolvedBackend"]; got != "host" {
		t.Fatalf("ResolvedBackend = %#v, want host", got)
	}
	if got := report["Route"]; got != "host" {
		t.Fatalf("Route = %#v, want host", got)
	}
}

func TestRunSandboxSetupSubcommandAcceptsBackendOverride(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "setup",
		"-sandbox-backend", "host",
		"-store-dir", t.TempDir(),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox setup -sandbox-backend host) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox setup output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxFixSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "fix",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox fix) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox fix output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxResetSubcommandTextOutput(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "reset",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox reset) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox reset output = %q, want requested host backend", out.String())
	}
}

func TestRunSandboxCleanSubcommandAliasesReset(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	var out bytes.Buffer
	var errBuf bytes.Buffer
	err := run(context.Background(), []string{
		"sandbox", "clean",
		"-sandbox-backend", "host",
		"-store-dir", cliTestStoreDir(t),
		"-workspace-key", "sandbox-ws",
		"-workspace-cwd", t.TempDir(),
	}, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		t.Fatalf("run(sandbox clean) error = %v; stderr=%q", err, errBuf.String())
	}
	if !strings.Contains(out.String(), "sandbox_requested_backend: host") {
		t.Fatalf("sandbox clean output = %q, want requested host backend", out.String())
	}
}

func capturedCLITool(tools []struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}, name string) bool {
	for _, item := range tools {
		if item.Function.Name == name {
			return true
		}
	}
	return false
}

func capturedAnthropicTool(tools []struct {
	Name string `json:"name"`
}, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func capturedGeminiTool(tools []struct {
	FunctionDeclarations []struct {
		Name string `json:"name"`
	} `json:"functionDeclarations"`
}, name string) bool {
	for _, tool := range tools {
		for _, declaration := range tool.FunctionDeclarations {
			if declaration.Name == name {
				return true
			}
		}
	}
	return false
}

func writeCodeFreeCredentialsForCLITest(t *testing.T, userID string, apiKey string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth_creds.json")
	raw, err := json.Marshal(map[string]string{
		"id_token": userID,
		"apikey":   encryptCodeFreeAPIKeyForCLITest(t, apiKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptCodeFreeAPIKeyForCLITest(t *testing.T, apiKey string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte("Xtpa6sS&+D.NAo%CP8LA:7pk"))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(apiKey)
	padding := block.BlockSize() - len(plain)%block.BlockSize()
	for i := 0; i < padding; i++ {
		plain = append(plain, byte(padding))
	}
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte("%1KJIrl3!XUxr04V")).CryptBlocks(ciphertext, plain)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

type fakeHandle struct {
	events    chan kernel.EventEnvelope
	submits   []kernel.SubmitRequest
	closed    bool
	cancelled bool
}

func newFakeHandle(events []kernel.EventEnvelope) *fakeHandle {
	ch := make(chan kernel.EventEnvelope, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return &fakeHandle{events: ch}
}

func (h *fakeHandle) HandleID() string { return "h1" }
func (h *fakeHandle) RunID() string    { return "r1" }
func (h *fakeHandle) TurnID() string   { return "t1" }
func (h *fakeHandle) SessionRef() session.SessionRef {
	return session.SessionRef{SessionID: "s1"}
}
func (h *fakeHandle) CreatedAt() time.Time { return time.Time{} }
func (h *fakeHandle) Events() <-chan kernel.EventEnvelope {
	return h.events
}
func (h *fakeHandle) EventsAfter(string) ([]kernel.EventEnvelope, string, error) {
	return nil, "", nil
}
func (h *fakeHandle) Submit(_ context.Context, req kernel.SubmitRequest) error {
	h.submits = append(h.submits, req)
	return nil
}
func (h *fakeHandle) Cancel() kernel.CancelResult {
	h.cancelled = true
	return kernel.CancelResult{Status: kernel.CancelStatusCancelled}
}
func (h *fakeHandle) Close() error {
	h.closed = true
	return nil
}
