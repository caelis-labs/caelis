package codex

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

// TestLiveCodexServiceTiers uses a disposable Codex home and two bounded prompts.
// It verifies wire requests and resumed backend state, never latency or self-report.
func TestLiveCodexServiceTiers(t *testing.T) {
	if os.Getenv("CAELIS_CODEX_TIER_E2E") != "1" {
		t.Skip("set CAELIS_CODEX_TIER_E2E=1 for two real Codex turns")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	home := t.TempDir()
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("CAELIS_CODEX_AUTH_FILE")
	if source == "" {
		source = filepath.Join(userHome, ".codex", "auth.json")
	}
	auth, err := os.ReadFile(source)
	if err != nil {
		t.Fatal("Codex authentication file unavailable")
	}
	if err = os.WriteFile(filepath.Join(home, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "codex", "app-server")
	command.Env = append(os.Environ(), "CODEX_HOME="+home)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = output.Close(); _ = command.Process.Kill(); _ = command.Wait() }()
	evidence := &tierWireEvidence{}
	backend, err := NewBackend(ctx, io.TeeReader(output, &tierWireTap{evidence: evidence}), io.MultiWriter(input, &tierWireTap{evidence: evidence}))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	toAgentReader, toAgentWriter := io.Pipe()
	toClientReader, toClientWriter := io.Pipe()
	defer toAgentReader.Close()
	defer toAgentWriter.Close()
	defer toClientReader.Close()
	defer toClientWriter.Close()
	go func() {
		_ = backend.ServeACP(ctx, ConnectionOptions{ConnectionID: "live-tier-test", Workspace: WorkspacePolicy{AllowedRoots: []string{home}, WritableRoots: []string{home}}}, toAgentReader, toClientWriter)
	}()
	recorder := &recordingNoticeClient{}
	client := acp.NewClientSideConnection(recorder, toAgentWriter, toClientReader)
	defer client.Close()
	if _, err = client.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersion(acp.WireProtocolVersion)}); err != nil {
		t.Fatal(err)
	}
	opened, err := client.NewSession(ctx, acp.NewSessionRequest{Cwd: home, McpServers: []acp.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	modelID := os.Getenv("CAELIS_CODEX_TIER_MODEL")
	if modelID == "" {
		modelID = "gpt-5.6-luna"
	}
	set := func(id, value string) []acp.SessionConfigOption {
		t.Helper()
		request := acp.SetSessionConfigOptionRequest{}
		raw, _ := json.Marshal(map[string]any{"sessionId": opened.SessionId, "configId": id, "value": value})
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		result, err := client.SetSessionConfigOption(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		return result.ConfigOptions
	}
	set("model", modelID)
	for _, tier := range []string{"priority", "default"} {
		set("service_tier", tier)
		if _, err = client.Prompt(ctx, acp.PromptRequest{SessionId: opened.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock("Reply with OK only. Do not call tools or access files.")}}); err != nil {
			t.Fatal(err)
		}
		if _, err = client.CloseSession(ctx, acp.CloseSessionRequest{SessionId: opened.SessionId}); err != nil {
			t.Fatal(err)
		}
		resumed, err := client.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: opened.SessionId, Cwd: home, McpServers: []acp.McpServer{}})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, option := range resumed.ConfigOptions {
			if option.Select != nil && string(option.Select.Id) == configIDServiceTier {
				found = true
				if string(option.Select.CurrentValue) != tier {
					t.Fatalf("resumed tier = %q, want %s", option.Select.CurrentValue, tier)
				}
			}
		}
		if !found {
			t.Fatal("resumed session lost service tier capability")
		}
	}
	records := evidence.snapshot()
	var starts []string
	for _, r := range records {
		if r.Method == "turn/start" {
			starts = append(starts, r.Tier)
		}
	}
	if strings.Join(starts, ",") != "priority,default" {
		t.Fatalf("wire tiers = %v", starts)
	}
	if path := os.Getenv("CAELIS_CODEX_TIER_E2E_OUT"); path != "" {
		data, _ := json.MarshalIndent(records, "", "  ")
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type tierWireRecord struct {
	Method   string `json:"method,omitempty"`
	Tier     string `json:"service_tier"`
	Response bool   `json:"response,omitempty"`
}
type tierWireEvidence struct {
	mu      sync.Mutex
	records []tierWireRecord
}

type tierWireTap struct {
	evidence *tierWireEvidence
	pending  string
}

func (tap *tierWireTap) Write(data []byte) (int, error) {
	e := tap.evidence
	e.mu.Lock()
	defer e.mu.Unlock()
	tap.pending += string(data)
	for {
		line, rest, found := strings.Cut(tap.pending, "\n")
		if !found {
			break
		}
		tap.pending = rest
		var packet struct {
			Method string `json:"method"`
			Params struct {
				ServiceTier *string `json:"serviceTier"`
			} `json:"params"`
			Result map[string]json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(line), &packet) != nil {
			continue
		}
		if packet.Method == "turn/start" && packet.Params.ServiceTier != nil {
			e.records = append(e.records, tierWireRecord{Method: packet.Method, Tier: *packet.Params.ServiceTier})
		}
		if raw, ok := packet.Result["serviceTier"]; ok {
			var tier string
			_ = json.Unmarshal(raw, &tier)
			e.records = append(e.records, tierWireRecord{Tier: tier, Response: true})
		}
	}
	return len(data), nil
}
func (e *tierWireEvidence) snapshot() []tierWireRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]tierWireRecord(nil), e.records...)
}
