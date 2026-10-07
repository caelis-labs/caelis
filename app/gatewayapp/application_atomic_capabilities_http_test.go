package gatewayapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func atomicMCPAudit(service, action string) {
	path := os.Getenv("CAELIS_ATOMIC_MCP_AUDIT")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = fmt.Fprintf(file, "%s:%s\n", service, action)
		_ = file.Close()
	}
}

func runAtomicMCPHelper(t *testing.T, service string) {
	if os.Getenv("CAELIS_ATOMIC_MCP_HELPER") != "1" {
		return
	}
	atomicMCPAudit(service, "start")
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: service, Version: "1.0.0"}, nil)
	mcpsdk.AddTool[map[string]any, any](server, &mcpsdk.Tool{Name: "lookup", Description: "Look up a synthetic " + service + " value"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ map[string]any) (*mcpsdk.CallToolResult, any, error) {
			atomicMCPAudit(service, "call")
			if service == "callfail" {
				os.Exit(3)
			}
			return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: service + "_RESULT"}}}, nil, nil
		})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
	atomicMCPAudit(service, "stop")
}

func TestAtomicDocumentsMCPHelper(t *testing.T) { runAtomicMCPHelper(t, "documents") }
func TestAtomicUtilitiesMCPHelper(t *testing.T) { runAtomicMCPHelper(t, "utilities") }
func TestAtomicCallFailMCPHelper(t *testing.T)  { runAtomicMCPHelper(t, "callfail") }
func TestAtomicFailedMCPHelper(t *testing.T) {
	if os.Getenv("CAELIS_ATOMIC_MCP_HELPER") == "1" {
		atomicMCPAudit("failed", "start")
		os.Exit(2)
	}
}

type atomicCapabilityProvider struct {
	mu       sync.Mutex
	requests [][]byte
	first    sync.Once
	started  chan struct{}
	hold     <-chan struct{}
}

func (p *atomicCapabilityProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.requests = append(p.requests, append([]byte(nil), raw...))
	p.mu.Unlock()
	if p.hold != nil {
		p.first.Do(func() { close(p.started) })
		select {
		case <-p.hold:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	var payload struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	latestUser, latestTool := "", ""
	for _, message := range payload.Messages {
		if message.Role == "user" {
			latestUser = fmt.Sprint(message.Content)
			latestTool = ""
		}
		if message.Role == "assistant" && len(message.ToolCalls) != 0 {
			latestTool = message.ToolCalls[len(message.ToolCalls)-1].Function.Name
		}
	}
	name, args := "", ""
	switch {
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "CALLFAIL"):
		name, args = "callfail__lookup", `{}`
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "DOC"):
		name, args = "documents__lookup", `{}`
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "UTIL"):
		name, args = "utilities__lookup", `{}`
	case latestTool != "":
	case strings.Contains(latestUser, "SKILL"):
		name, args = "Skill", `{"name":"atomic-skill"}`
	case strings.Contains(latestUser, "CALLFAIL"):
		name, args = "ToolSearch", `{"query":"callfail lookup"}`
	case strings.Contains(latestUser, "DOC"):
		name, args = "ToolSearch", `{"query":"documents lookup"}`
	case strings.Contains(latestUser, "UTIL"):
		name, args = "ToolSearch", `{"query":"utilities lookup"}`
	}
	message := map[string]any{"role": "assistant", "content": "ATOMIC_DIALOGUE_OK"}
	finish := "stop"
	if name != "" {
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"id": "atomic-provider-call", "index": 0, "type": "function",
			"function": map[string]any{"name": name, "arguments": args},
		}}}
		finish = "tool_calls"
	}
	encoded, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: " + string(encoded) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func (p *atomicCapabilityProvider) snapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var b strings.Builder
	for _, request := range p.requests {
		b.Write(request)
		b.WriteByte('\n')
	}
	return b.String()
}

func (p *atomicCapabilityProvider) lastTools() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	var request struct {
		Tools json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(p.requests[len(p.requests)-1], &request)
	return string(request.Tools)
}

func (p *atomicCapabilityProvider) lastRequest() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	return string(p.requests[len(p.requests)-1])
}

func (p *atomicCapabilityProvider) requestAt(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index < 0 || index >= len(p.requests) {
		return ""
	}
	return string(p.requests[index])
}

func atomicAudit(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}

func waitAtomicAudit(t *testing.T, path, marker string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(atomicAudit(path), marker) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("MCP service %q did not start; audit: %s", marker, atomicAudit(path))
}

func TestApplicationAtomicCapabilitiesHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "mcp-audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".agents", "skills", "ambient"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agents", "skills", "ambient", "SKILL.md"), []byte("---\nname: ambient\ndescription: AMBIENT_LEAK\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skills := filepath.Join(root, "authorized-skills")
	if err := os.MkdirAll(filepath.Join(skills, "atomic"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "atomic", "SKILL.md"), []byte("---\nname: atomic-skill\ndescription: Synthetic authorized skill.\n---\nATOMIC_SKILL_BODY_731\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &atomicCapabilityProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "atomic-model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{
		Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY",
	}}); err != nil {
		t.Fatal(err)
	}
	info, err := host.host.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, application.CapabilityAtomicCapabilities) {
		t.Fatalf("atomic capability missing: %+v, %v", info, err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "atomic", filepath.Join(root, "app.credential"))
	profile := applicationHTTPProfile()
	profile.SkillDirs = []string{skills}
	profile.MCPServers = []application.MCPServer{
		{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root},
		{Name: "utilities", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicUtilitiesMCPHelper$"}, WorkDir: root},
	}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "atomic-create"}, Profile: profile})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	configuration, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || len(configuration.Profile.MCPServers) != 2 || len(configuration.Profile.SkillDirs) != 1 {
		t.Fatalf("configuration = %+v, %v", configuration, err)
	}
	inactive, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || inactive.ConfigurationRevision != "1" || len(inactive.Servers) != 2 || inactive.Servers[0].Status != "inactive" || inactive.Servers[1].Status != "inactive" {
		t.Fatalf("status read activated a service or lost a domain: %+v, %v", inactive, err)
	}
	other, _ := registerApplicationHTTP(t, ctx, host, "other-atomic", filepath.Join(root, "other.credential"))
	if _, err := other.ApplicationMCPStatus(ctx, created.SessionID); err == nil {
		t.Fatal("another application read MCP service health")
	}
	// The real HTTP -> Control -> Runtime -> SDK path must expose metadata and
	// load the Skill body only after the model calls the Skill tool.
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-skill", "SKILL")
	first := provider.requestAt(0)
	if !strings.Contains(first, "atomic-skill") || !strings.Contains(first, "ApplicationLookup") || !strings.Contains(first, "ToolSearch") || !strings.Contains(first, "Skill") || strings.Contains(first, "ATOMIC_SKILL_BODY_731") {
		t.Fatalf("first model request lacked assembled metadata/tools or inlined Skill body: %s", first)
	}
	requests := provider.snapshot()
	if !strings.Contains(requests, "atomic-skill") || !strings.Contains(requests, "ATOMIC_SKILL_BODY_731") || strings.Contains(requests, "AMBIENT_LEAK") {
		t.Fatalf("Skill metadata/body or isolation missing: %s", requests)
	}
	waitAtomicAudit(t, audit, "documents:start")
	waitAtomicAudit(t, audit, "utilities:start")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-doc", "DOC")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-util", "UTIL")
	if got := atomicAudit(audit); strings.Count(got, "documents:call") != 1 || strings.Count(got, "utilities:call") != 1 {
		t.Fatalf("independent MCP calls: %s; requests: %s", got, provider.snapshot())
	}
	missing := []string{filepath.Join(root, "missing")}
	if _, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "atomic-bad", ExpectedConfigurationRevision: configuration.Revision, Patch: application.ConfigurationPatch{SkillDirs: &missing}}); err == nil {
		t.Fatal("invalid reconfiguration was committed")
	}
	unchanged, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || unchanged.Revision != configuration.Revision {
		t.Fatalf("failed update changed configuration: %+v %v", unchanged, err)
	}
	emptyServices := []application.MCPServer{}
	emptyDirs := []string{}
	disabled, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "atomic-disable", ExpectedConfigurationRevision: configuration.Revision, Patch: application.ConfigurationPatch{MCPServers: &emptyServices, SkillDirs: &emptyDirs}})
	if err != nil || disabled.Revision != configuration.Revision+1 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	disabledStatus, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || len(disabledStatus.Servers) != 0 || disabledStatus.ConfigurationRevision != "2" {
		t.Fatalf("disabled desired service status = %+v, %v", disabledStatus, err)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-basic", "BASIC")
	tools := provider.lastTools()
	if strings.Contains(tools, "\"name\":\"Skill\"") || strings.Contains(tools, "\"name\":\"ToolSearch\"") || strings.Contains(tools, "documents__lookup") {
		t.Fatalf("disabled capabilities remained in new model request: %s", tools)
	}
	reenabled, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{OperationID: "atomic-reenable", ExpectedConfigurationRevision: disabled.Revision, Patch: application.ConfigurationPatch{MCPServers: &profile.MCPServers, SkillDirs: &profile.SkillDirs}})
	if err != nil || reenabled.Revision != disabled.Revision+1 {
		t.Fatalf("reenable = %+v, %v", reenabled, err)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-warm", "BASIC")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "atomic-doc-again", "DOC")
	if got := atomicAudit(audit); strings.Count(got, "documents:call") != 2 {
		t.Fatalf("reenabled service call count: %s; requests: %s", got, provider.snapshot())
	}
	worker, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "atomic-worker"}, CWD: workspace, Model: "openai-compatible/gpt-4.1"})
	if err != nil || worker.SessionID == "" {
		t.Fatalf("create ordinary Worker = %+v, %v", worker, err)
	}
	if _, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "atomic-worker-prompt", SessionID: worker.SessionID}, Input: "BASIC"}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, worker.SessionID)
	if last := provider.lastRequest(); strings.Contains(last, "atomic-skill") || strings.Contains(last, "documents__lookup") || strings.Contains(last, "utilities__lookup") {
		t.Fatalf("ordinary Worker inherited Application capabilities: %s", last)
	}
	beforeRecoveryCalls := strings.Count(atomicAudit(audit), "documents:call")
	host.close(t)
	host = nil
	recoveredHost := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer recoveredHost.close(t)
	credential, err := os.ReadFile(filepath.Join(root, "app.credential"))
	if err != nil {
		t.Fatal(err)
	}
	recovered := recoveredHost.app(string(credential))
	feed, err := recovered.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := feed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(atomicAudit(audit), "documents:call"); got != beforeRecoveryCalls {
		t.Fatalf("reconnect replayed an MCP effect: before=%d after=%d", beforeRecoveryCalls, got)
	}
	recoveryStatus, err := recovered.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || recoveryStatus.ConfigurationRevision != "3" {
		t.Fatalf("recovered desired configuration = %+v, %v", recoveryStatus, err)
	}
	promptAtomicCapabilityClient(t, ctx, recovered, created.SessionID, "atomic-recovery-warm", "BASIC")
	waitAtomicAudit(t, audit, "documents:start")
	promptAtomicCapabilityClient(t, ctx, recovered, created.SessionID, "atomic-recovery-doc", "DOC")
	if got := strings.Count(atomicAudit(audit), "documents:call"); got != beforeRecoveryCalls+1 {
		t.Fatalf("recovered Session made %d document calls, want %d", got, beforeRecoveryCalls+1)
	}
}

func TestApplicationAtomicMCPFailureIsolationHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "mcp-audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	hold := make(chan struct{})
	provider := &atomicCapabilityProvider{hold: hold, started: make(chan struct{})}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "failure-model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{
		Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY",
	}}); err != nil {
		t.Fatal(err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "failure", filepath.Join(root, "app.credential"))
	profile := applicationHTTPProfile()
	profile.MCPServers = []application.MCPServer{
		{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root},
		{Name: "failed", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicFailedMCPHelper$"}, WorkDir: root},
		{Name: "utilities", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicUtilitiesMCPHelper$"}, WorkDir: root},
		{Name: "callfail", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicCallFailMCPHelper$"}, WorkDir: root},
	}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "failure-create"}, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "failure-prompt", SessionID: created.SessionID}, Input: "BASIC"}, SourceKind: "user"})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("basic prompt = %+v, %v", result, err)
	}
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	deadline := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	for {
		health, err := client.ApplicationMCPStatus(ctx, created.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]string{}
		for _, server := range health.Servers {
			states[server.Name] = server.Status
		}
		if states["documents"] == "running" && states["utilities"] == "running" && states["callfail"] == "running" && states["failed"] == "failed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("MCP service health did not isolate failure: %+v; audit: %s", health, atomicAudit(audit))
		case <-deadline.C:
		}
	}
	close(hold)
	waitApplicationHTTPIdle(t, ctx, client, created.SessionID)
	if len(provider.snapshot()) == 0 {
		t.Fatal("basic dialogue did not reach deterministic provider")
	}
	if got := atomicAudit(audit); strings.Count(got, "documents:call") != 0 || strings.Count(got, "utilities:call") != 0 {
		t.Fatalf("service effects invented during basic dialogue: %s", got)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "failure-call", "CALLFAIL")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "failure-healthy", "DOC")
	if got := atomicAudit(audit); strings.Count(got, "callfail:call") != 1 || strings.Count(got, "documents:call") != 1 {
		t.Fatalf("call failure blocked an independent service: %s; requests: %s", got, provider.snapshot())
	}
}

func promptAtomicCapabilityClient(t *testing.T, ctx context.Context, client *httpclient.Client, session, operation, input string) {
	t.Helper()
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: session, OperationID: operation}, Input: input}, SourceKind: "user",
	})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("prompt %s = %+v, %v", operation, result, err)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
}
