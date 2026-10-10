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

	"github.com/caelis-labs/caelis/control/agentbinding"
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
		_, _ = fmt.Fprintf(file, "%s:%s:%d\n", service, action, os.Getpid())
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
			if service == "blocking" {
				release := os.Getenv("CAELIS_ATOMIC_MCP_RELEASE")
				for {
					if _, err := os.Stat(release); err == nil {
						break
					}
					time.Sleep(15 * time.Millisecond)
				}
			}
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
func TestAtomicBlockingMCPHelper(t *testing.T)  { runAtomicMCPHelper(t, "blocking") }
func TestAtomicFailedMCPHelper(t *testing.T) {
	if os.Getenv("CAELIS_ATOMIC_MCP_HELPER") == "1" {
		atomicMCPAudit("failed", "start")
		os.Exit(2)
	}
}

func TestApplicationMCPAlwaysRespectsAuthenticatedConfigurationOwnerHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "shared-workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(root, "mcp-audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, &atomicCapabilityProvider{})
	defer host.close(t)
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "owner-model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{
		Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY",
	}}); err != nil {
		t.Fatal(err)
	}
	a, _ := registerApplicationHTTP(t, ctx, host, "owner-a", filepath.Join(root, "a.credential"))
	b, _ := registerApplicationHTTP(t, ctx, host, "owner-b", filepath.Join(root, "b.credential"))
	profile := applicationHTTPProfile()
	profile.Workspace.CWD = workspace
	profile.MCPServers = []application.MCPServer{{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root}}
	create := func(client *httpclient.Client, operation string) string {
		t.Helper()
		result, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: operation}, Profile: profile})
		if err != nil || result.SessionID == "" {
			t.Fatalf("create %s: %+v, %v", operation, result, err)
		}
		return result.SessionID
	}
	aFirst, aSecond, bFirst := create(a, "owner-a-first"), create(a, "owner-a-second"), create(b, "owner-b-first")
	if got := promptAtomicCapabilityClientWithMCPApproval(t, ctx, a, aFirst, "a-always", "DOC", "allow_always"); got != 1 {
		t.Fatalf("A approval count = %d, want 1", got)
	}
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 1 {
		t.Fatalf("A remote calls = %d, want 1", got)
	}
	result, err := b.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: bFirst, OperationID: "b-cancel"}, Input: "DOC"}, SourceKind: "user"})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("B prompt = %+v, %v", result, err)
	}
	var pending *appserver.ActiveApproval
	for pending == nil {
		state, err := b.InspectSession(ctx, appserver.StateRequest{SessionID: bFirst})
		if err != nil {
			t.Fatal(err)
		}
		pending = state.Approval.Active
		if pending == nil {
			select {
			case <-ctx.Done():
				t.Fatal("independent Application skipped MCP approval")
			case <-time.After(15 * time.Millisecond):
			}
		}
	}
	if pending.Permission == nil || len(pending.Permission.Options) != 4 || pending.Permission.Options[2].ID != "allow_always" {
		t.Fatalf("B MCP approval lost four options: %+v", pending)
	}
	request := appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "foreign-b-approval", SessionID: bFirst}, Target: pending.Target,
		ApprovalRequestID: string(pending.RequestID), Outcome: "selected", OptionID: "allow_always", Approved: true}
	if _, err := a.ResolveApproval(ctx, request); err == nil {
		t.Fatal("A was able to resolve B's pending approval")
	}
	request.OperationID = "b-cancel-approval"
	request.OptionID, request.Approved = "cancel", false
	if _, err := b.ResolveApproval(ctx, request); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, b, bFirst)
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 1 {
		t.Fatalf("B cancel executed remote call: %d", got)
	}
	if got := promptAtomicCapabilityClientWithMCPApproval(t, ctx, a, aSecond, "a-same-owner", "DOC", ""); got != 0 {
		t.Fatalf("same Application owner was re-prompted: %d", got)
	}
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 2 {
		t.Fatalf("same owner failed to reuse grant: calls=%d", got)
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
		Stream bool `json:"stream"`
		Tools  []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
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
	if len(payload.Tools) == 1 && payload.Tools[0].Function.Name == "InspectToolSchema" {
		selected := ""
		switch {
		case strings.Contains(latestUser, "CALLFAIL"):
			selected = "callfail__lookup"
		case strings.Contains(latestUser, "BLOCK"):
			selected = "blocking__lookup"
		case strings.Contains(latestUser, "DOC"):
			selected = "documents__lookup"
		case strings.Contains(latestUser, "UTIL"):
			selected = "utilities__lookup"
		}
		selection, _ := json.Marshal(map[string]any{"tools": []string{selected}})
		if selected == "" {
			selection = []byte(`{"tools":[]}`)
		}
		if !payload.Stream {
			return nil, fmt.Errorf("ToolSearch selector must request a stream")
		}
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": string(selection)}, "finish_reason": "stop"}}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("data: " + string(encoded) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
	}
	switch {
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "CALLFAIL"):
		name, args = "callfail__lookup", `{}`
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "BLOCK"):
		name, args = "blocking__lookup", `{}`
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "DOC"):
		name, args = "documents__lookup", `{}`
	case latestTool == "ToolSearch" && strings.Contains(latestUser, "UTIL"):
		name, args = "utilities__lookup", `{}`
	case latestTool != "":
	case strings.Contains(latestUser, "HEALTHY_SKILL"):
		name, args = "Skill", `{"name":"healthy-skill"}`
	case strings.Contains(latestUser, "BODY_SKILL"):
		name, args = "Skill", `{"name":"body-skill"}`
	case strings.Contains(latestUser, "SKILL"):
		name, args = "Skill", `{"name":"atomic-skill"}`
	case strings.Contains(latestUser, "CALLFAIL"):
		name, args = "ToolSearch", `{"query":"callfail lookup"}`
	case strings.Contains(latestUser, "BLOCK"):
		name, args = "ToolSearch", `{"query":"blocking lookup"}`
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

func waitAtomicAuditCount(t *testing.T, path, marker string, want int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(atomicAudit(path), marker) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("audit %q count = %d, want %d; audit: %s", marker, strings.Count(atomicAudit(path), marker), want, atomicAudit(path))
}

func atomicAssertLiveService(t *testing.T, path, service string, want int) {
	t.Helper()
	audit := atomicAudit(path)
	starts, stops := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(audit), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 3 || parts[0] != service {
			continue
		}
		switch parts[1] {
		case "start":
			starts[parts[2]] = true
		case "stop":
			stops[parts[2]] = true
		}
	}
	live := 0
	for pid := range starts {
		if !stops[pid] {
			live++
		}
	}
	if live != want {
		t.Fatalf("%s live audited processes = %d, want %d; audit: %s", service, live, want, audit)
	}
}

func writeAtomicSkill(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: Synthetic %s instructions.\n---\n%s\n", name, name, body)
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func atomicSkillStatusesByPath(status application.MCPStatus) map[string]application.SkillStatus {
	byPath := make(map[string]application.SkillStatus, len(status.Skills))
	for _, item := range status.Skills {
		byPath[item.Path] = item
	}
	return byPath
}

func TestApplicationAtomicSkillFailureIsolationHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	dir := filepath.Join(root, "selected-skills")
	healthy := filepath.Join(dir, "healthy")
	corrupt := filepath.Join(dir, "corrupt")
	body := filepath.Join(dir, "body")
	vanishedDir := filepath.Join(root, "vanished-dir")
	vanishedChild := filepath.Join(vanishedDir, "vanished")
	missing := filepath.Join(root, "explicit-root")
	writeAtomicSkill(t, healthy, "healthy-skill", "HEALTHY_BODY_953")
	writeAtomicSkill(t, corrupt, "corrupt-skill", "CORRUPT_BODY_217")
	writeAtomicSkill(t, body, "body-skill", "BODY_BEFORE_REMOVAL_418")
	writeAtomicSkill(t, vanishedChild, "vanished-skill", "VANISHED_BODY_612")
	writeAtomicSkill(t, missing, "missing-skill", "MISSING_BODY_809")
	provider := &atomicCapabilityProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	hostStatus, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "skill-isolation-model", ExpectedRevision: &hostStatus.Configuration.Revision}, Config: appserver.ConnectConfig{
		Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY",
	}}); err != nil {
		t.Fatal(err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "skill-isolation", filepath.Join(root, "app.credential"))
	profile := applicationHTTPProfile()
	profile.SkillDirs = []string{dir, vanishedDir}
	profile.SkillRoots = []string{missing}
	profile.MCPServers = []application.MCPServer{
		{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root},
		{Name: "utilities", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicUtilitiesMCPHelper$"}, WorkDir: root},
	}
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "skill-isolation-create"}, Profile: profile})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	before, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || len(before.Skills) != 3 || before.Skills[0].Status != "inactive" || before.Skills[1].Status != "inactive" || before.Skills[2].Status != "inactive" {
		t.Fatalf("status read activated Skills: %+v, %v", before, err)
	}
	// Both failures happen after a valid commit but before first assembly.
	if err := os.Remove(filepath.Join(missing, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, "SKILL.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(vanishedDir); err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-basic", "BASIC")
	first := provider.requestAt(0)
	if !strings.Contains(first, "healthy-skill") || !strings.Contains(first, "body-skill") || strings.Contains(first, "corrupt-skill") || strings.Contains(first, "missing-skill") || strings.Contains(first, "vanished-skill") || strings.Contains(first, "HEALTHY_BODY_953") {
		t.Fatalf("model-facing Skill catalog was incomplete or included failed/body content: %s", first)
	}
	status, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || status.ConfigurationRevision != "1" {
		t.Fatalf("skill status = %+v, %v", status, err)
	}
	byPath := atomicSkillStatusesByPath(status)
	if byPath[dir].Status != "ready" || byPath[healthy].Status != "ready" || byPath[body].Status != "ready" || byPath[corrupt].Status != "failed" || byPath[missing].Status != "failed" || byPath[vanishedDir].Status != "failed" || byPath[corrupt].Warning == "" || byPath[missing].Warning == "" || byPath[vanishedDir].Warning == "" {
		t.Fatalf("per-Skill failure was hidden or healthy Skills lost: %+v", status.Skills)
	}
	invalidInstructions := "Cannot commit while selected Skill metadata is broken."
	if _, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{
		OperationID: "skill-isolation-reject-invalid", ExpectedConfigurationRevision: 1,
		Patch: application.ConfigurationPatch{Instructions: &invalidInstructions},
	}); err == nil {
		t.Fatal("deterministically invalid Skill metadata was committed")
	}
	unchanged, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil || unchanged.Revision != 1 {
		t.Fatalf("invalid update changed revision: %+v, %v", unchanged, err)
	}
	deadline := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	for {
		status, err = client.ApplicationMCPStatus(ctx, created.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Servers) == 2 && status.Servers[0].Status == "running" && status.Servers[1].Status == "running" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("healthy MCP services did not initialize: %+v", status.Servers)
		case <-deadline.C:
		}
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-healthy", "HEALTHY_SKILL")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-doc", "DOC")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-util", "UTIL")
	if got := atomicAudit(audit); strings.Count(got, "documents:call:") != 1 || strings.Count(got, "utilities:call:") != 1 {
		t.Fatalf("healthy MCP services were blocked: %s", got)
	}
	if !strings.Contains(provider.snapshot(), "HEALTHY_BODY_953") {
		t.Fatal("healthy Skill body was not loaded on demand")
	}
	if err := os.Remove(filepath.Join(body, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-body-fail", "BODY_SKILL")
	if got := provider.lastRequest(); !strings.Contains(got, "body-skill") || !strings.Contains(got, "load skill") {
		t.Fatalf("on-demand body failure was not returned to the model: %.2000s", got)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-basic-after-fail", "BASIC")
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-healthy-after-fail", "HEALTHY_SKILL")
	worker, err := client.CreateWorker(ctx, appserver.CreateWorkerRequest{WriteBase: appserver.WriteBase{OperationID: "skill-isolation-worker"}, CWD: root, Model: "openai-compatible/gpt-4.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "skill-isolation-worker-prompt", SessionID: worker.SessionID}, Input: "BASIC"}); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, worker.SessionID)
	if last := provider.lastRequest(); strings.Contains(last, "healthy-skill") || strings.Contains(last, "documents__lookup") || strings.Contains(last, "utilities__lookup") {
		t.Fatalf("ordinary Worker inherited application abilities: %s", last)
	}
	writeAtomicSkill(t, corrupt, "corrupt-skill", "CORRUPT_BODY_RESTORED_217")
	writeAtomicSkill(t, missing, "missing-skill", "MISSING_BODY_RESTORED_809")
	writeAtomicSkill(t, body, "body-skill", "BODY_RESTORED_418")
	writeAtomicSkill(t, vanishedChild, "vanished-skill", "VANISHED_BODY_RESTORED_612")
	configuration, err := client.ApplicationConfiguration(ctx, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	instructions := "Refresh authorized Skill metadata after restoring files."
	updated, err := client.UpdateApplicationConfiguration(ctx, created.SessionID, application.UpdateConfigurationRequest{
		OperationID: "skill-isolation-refresh", ExpectedConfigurationRevision: configuration.Revision,
		Patch: application.ConfigurationPatch{Instructions: &instructions},
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("explicit recovery revision = %+v, %v", updated, err)
	}
	pending, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || pending.ConfigurationRevision != "2" || len(pending.Skills) != 3 || pending.Skills[0].Status != "inactive" {
		t.Fatalf("new revision status reused old catalog: %+v, %v", pending, err)
	}
	promptAtomicCapabilityClient(t, ctx, client, created.SessionID, "skill-isolation-refreshed", "BASIC")
	recovered, err := client.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || recovered.ConfigurationRevision != "2" {
		t.Fatalf("recovered status = %+v, %v", recovered, err)
	}
	for _, path := range []string{healthy, corrupt, body, missing, vanishedChild} {
		if got := atomicSkillStatusesByPath(recovered)[path]; got.Status != "ready" || got.Name == "" {
			t.Fatalf("repaired Skill %s = %+v", path, got)
		}
	}
	// A Skills-only selection with no surviving Skill must still admit dialogue.
	only := filepath.Join(root, "skills-only")
	writeAtomicSkill(t, only, "only-skill", "ONLY_BODY_274")
	onlyProfile := applicationHTTPProfile()
	onlyProfile.SkillRoots = []string{only}
	onlySession, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "skill-isolation-only-create"}, Profile: onlyProfile,
	})
	if err != nil || onlySession.SessionID == "" {
		t.Fatalf("Skills-only create = %+v, %v", onlySession, err)
	}
	onlyFeed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: onlySession.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer onlyFeed.Subscription.Close()
	if err := os.Remove(filepath.Join(only, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, client, onlySession.SessionID, "skill-isolation-only-basic", "BASIC")
	onlyStatus, err := client.ApplicationMCPStatus(ctx, onlySession.SessionID)
	if err != nil || len(onlyStatus.Servers) != 0 || len(onlyStatus.Skills) != 1 || onlyStatus.Skills[0].Status != "failed" {
		t.Fatalf("Skills-only failure blocked dialogue or was hidden: %+v, %v", onlyStatus, err)
	}
	if last := provider.lastRequest(); strings.Contains(last, "only-skill") || strings.Contains(provider.lastTools(), "\"name\":\"Skill\"") {
		t.Fatalf("failed Skills-only root entered the model request: %.2000s", last)
	}
}

func startAtomicLifecycleSession(t *testing.T, ctx context.Context, root string, provider http.RoundTripper, servers []application.MCPServer) (*applicationHTTPHost, *httpclient.Client, string) {
	t.Helper()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "lifecycle-model", ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{
		Provider: "openai-compatible", Model: "gpt-4.1", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY",
	}}); err != nil {
		t.Fatal(err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "lifecycle", filepath.Join(root, "app.credential"))
	profile := applicationHTTPProfile()
	profile.MCPServers = servers
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "lifecycle-create"}, Profile: profile})
	if err != nil || created.SessionID == "" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	return host, client, created.SessionID
}

func TestApplicationAtomicMCPRevisionLifecycleHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	server := application.MCPServer{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root}
	provider := &atomicCapabilityProvider{}
	host, client, session := startAtomicLifecycleSession(t, ctx, root, provider, []application.MCPServer{server})
	defer host.close(t)
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	promptAtomicCapabilityClient(t, ctx, client, session, "lifecycle-warm", "BASIC")
	waitAtomicAuditCount(t, audit, "documents:start:", 1)
	configuration, err := client.ApplicationConfiguration(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	hostStatus, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{
		OperationID: "lifecycle-second-model", ExpectedRevision: &hostStatus.Configuration.Revision,
	}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.2", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"}}); err != nil {
		t.Fatal(err)
	}
	modelName := "openai-compatible/gpt-4.2"
	configuration, err = client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
		OperationID: "lifecycle-model-only", ExpectedConfigurationRevision: configuration.Revision,
		Patch: application.ConfigurationPatch{Model: &modelName},
	})
	if err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, client, session, "lifecycle-after-model", "BASIC")
	if got := atomicAudit(audit); strings.Count(got, "documents:start:") != 1 || strings.Contains(got, "documents:stop:") {
		t.Fatalf("model-only revision restarted MCP: %s", got)
	}
	for i := 0; i < 3; i++ {
		instructions := fmt.Sprintf("revision %d", i)
		configuration, err = client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
			OperationID: fmt.Sprintf("lifecycle-instructions-%d", i), ExpectedConfigurationRevision: configuration.Revision,
			Patch: application.ConfigurationPatch{Instructions: &instructions},
		})
		if err != nil {
			t.Fatal(err)
		}
		status, err := client.ApplicationMCPStatus(ctx, session)
		if err != nil || len(status.Servers) != 1 || status.Servers[0].Status != "running" {
			t.Fatalf("same MCP selection lost resident health after unrelated revision: %+v, %v", status, err)
		}
		if server := status.Servers[0]; !slices.Equal(server.Tools, []string{"lookup"}) || len(server.ToolDetails) != 1 || server.ToolDetails[0].Name != "lookup" || !strings.Contains(server.ToolDetails[0].Description, "Look up a synthetic documents value") {
			t.Fatalf("ready status lost accepted tool description: %+v", server)
		}
		promptAtomicCapabilityClient(t, ctx, client, session, fmt.Sprintf("lifecycle-basic-%d", i), "BASIC")
		if got := atomicAudit(audit); strings.Count(got, "documents:start:") != i+1 || strings.Count(got, "documents:stop:") != i {
			t.Fatalf("unrelated revision restarted MCP: %s", got)
		}
		atomicAssertLiveService(t, audit, "documents", 1)
		empty := []application.MCPServer{}
		configuration, err = client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
			OperationID: fmt.Sprintf("lifecycle-disable-%d", i), ExpectedConfigurationRevision: configuration.Revision,
			Patch: application.ConfigurationPatch{MCPServers: &empty},
		})
		if err != nil {
			t.Fatal(err)
		}
		disabledStatus, err := client.ApplicationMCPStatus(ctx, session)
		if err != nil || disabledStatus.ConfigurationRevision != fmt.Sprint(configuration.Revision) || len(disabledStatus.Servers) != 0 {
			t.Fatalf("disabled revision retained public MCP tools: %+v, %v", disabledStatus, err)
		}
		waitAtomicAuditCount(t, audit, "documents:stop:", i+1)
		atomicAssertLiveService(t, audit, "documents", 0)
		promptAtomicCapabilityClient(t, ctx, client, session, fmt.Sprintf("lifecycle-disabled-%d", i), "BASIC")
		if strings.Contains(provider.lastTools(), "ToolSearch") {
			t.Fatal("disabled tool appeared in a new request")
		}
		servers := []application.MCPServer{server}
		configuration, err = client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
			OperationID: fmt.Sprintf("lifecycle-enable-%d", i), ExpectedConfigurationRevision: configuration.Revision,
			Patch: application.ConfigurationPatch{MCPServers: &servers},
		})
		if err != nil {
			t.Fatal(err)
		}
		pending, err := client.ApplicationMCPStatus(ctx, session)
		if err != nil || pending.ConfigurationRevision != fmt.Sprint(configuration.Revision) || len(pending.Servers) != 1 {
			t.Fatalf("reloaded generation reused stale descriptions: %+v, %v", pending, err)
		}
		if pending.Servers[0].Status != "running" && len(pending.Servers[0].ToolDetails) != 0 {
			t.Fatalf("unready generation exposed tool details: %+v", pending.Servers[0])
		}
		if pending.Servers[0].Status == "running" && strings.Count(atomicAudit(audit), "documents:start:") < i+2 {
			t.Fatalf("old generation leaked into status: %+v", pending.Servers[0])
		}
		promptAtomicCapabilityClient(t, ctx, client, session, fmt.Sprintf("lifecycle-reenabled-warm-%d", i), "BASIC")
		waitAtomicApplicationMCPRunning(t, ctx, client, session, "documents")
		reloaded, err := client.ApplicationMCPStatus(ctx, session)
		if err != nil || len(reloaded.Servers) != 1 || len(reloaded.Servers[0].ToolDetails) != 1 || reloaded.Servers[0].ToolDetails[0].Name != "lookup" {
			t.Fatalf("reloaded ready generation lost tool details: %+v, %v", reloaded, err)
		}
		promptAtomicCapabilityClient(t, ctx, client, session, fmt.Sprintf("lifecycle-doc-%d", i), "DOC")
		waitAtomicAuditCount(t, audit, "documents:start:", i+2)
		atomicAssertLiveService(t, audit, "documents", 1)
	}
	if got := atomicAudit(audit); strings.Count(got, "documents:call:") != 3 {
		t.Fatalf("MCP effects were duplicated: %s", got)
	}
}

func TestApplicationAtomicMCPPinnedCallAcrossRevisionHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	releasePath := filepath.Join(root, "release")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	t.Setenv("CAELIS_ATOMIC_MCP_RELEASE", releasePath)
	blocking := application.MCPServer{Name: "blocking", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicBlockingMCPHelper$"}, WorkDir: root}
	provider := &atomicCapabilityProvider{}
	host, client, session := startAtomicLifecycleSession(t, ctx, root, provider, []application.MCPServer{blocking})
	defer host.close(t)
	defer func() { _ = os.WriteFile(releasePath, []byte("release"), 0o600) }()
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	promptAtomicCapabilityClient(t, ctx, client, session, "pinned-warm", "BASIC")
	waitAtomicApplicationMCPRunning(t, ctx, client, session, "blocking")
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: session, OperationID: "pinned-old"}, Input: "BLOCK",
	}, SourceKind: "user"})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("old prompt = %+v, %v", result, err)
	}
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: session})
		if err != nil {
			t.Fatal(err)
		}
		if active := state.Approval.Active; active != nil {
			if active.Permission == nil || len(active.Permission.Options) != 4 || active.Permission.ToolCall.Name != "blocking__lookup" {
				t.Fatalf("blocking MCP approval = %+v", active)
			}
			_, err := client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "pinned-old-approval", SessionID: session},
				Target: active.Target, ApprovalRequestID: string(active.RequestID), Outcome: "selected", OptionID: "allow_once", Approved: true})
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("blocking MCP call never requested approval")
		case <-time.After(15 * time.Millisecond):
		}
	}
	waitAtomicAuditCount(t, audit, "blocking:call:", 1)
	// Track the exact process that accepted the blocked tool invocation.
	callPID := ""
	for _, line := range strings.Split(atomicAudit(audit), "\n") {
		pid, ok := strings.CutPrefix(line, "blocking:call:")
		if !ok {
			continue
		}
		if pid == "" || callPID != "" {
			t.Fatalf("expected one complete blocked-call PID; audit: %s", atomicAudit(audit))
		}
		callPID = pid
	}
	if callPID == "" {
		t.Fatalf("missing blocked-call PID; audit: %s", atomicAudit(audit))
	}
	callStart := "blocking:start:" + callPID + "\n"
	callStop := "blocking:stop:" + callPID + "\n"
	configuration, err := client.ApplicationConfiguration(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	utilities := []application.MCPServer{{Name: "utilities", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicUtilitiesMCPHelper$"}, WorkDir: root}}
	updated, err := client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
		OperationID: "pinned-change", ExpectedConfigurationRevision: configuration.Revision,
		Patch: application.ConfigurationPatch{MCPServers: &utilities},
	})
	if err != nil || updated.Revision != configuration.Revision+1 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if got := atomicAudit(audit); !strings.Contains(got, callStart) || strings.Contains(got, callStop) {
		t.Fatalf("MCP process holding admitted call is not alive: %s", got)
	}
	atomicAssertLiveService(t, audit, "blocking", 1)
	// This Session rejects concurrent inputs while the old Turn is active.
	// The conflict is a definite rejection, so no second effect is dispatched.
	newResult, newErr := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{PromptRequest: appserver.PromptRequest{
		WriteBase: appserver.WriteBase{SessionID: session, OperationID: "pinned-new"}, Input: "UTIL",
	}, SourceKind: "user"})
	if newErr == nil || newResult.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("concurrent prompt = %+v, %v; want definite conflict", newResult, newErr)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitApplicationHTTPIdle(t, ctx, client, session)
	waitAtomicAudit(t, audit, callStop)
	promptAtomicCapabilityClient(t, ctx, client, session, "pinned-new-after-old", "UTIL")
	waitAtomicAuditCount(t, audit, "utilities:call:", 1)
	if got := atomicAudit(audit); strings.Count(got, "blocking:call:") != 1 || strings.Count(got, "utilities:call:") != 1 {
		t.Fatalf("old or new effect replayed: %s", got)
	}
	atomicAssertLiveService(t, audit, "blocking", 0)
	atomicAssertLiveService(t, audit, "utilities", 1)
	if !strings.Contains(provider.snapshot(), "blocking_RESULT") || !strings.Contains(provider.snapshot(), "utilities_RESULT") {
		t.Fatalf("old response or new ability missing: %s", provider.snapshot())
	}
}

func TestApplicationAtomicMCPFailedUpdateConcurrentCloseHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	server := application.MCPServer{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root}
	host, client, session := startAtomicLifecycleSession(t, ctx, root, &atomicCapabilityProvider{}, []application.MCPServer{server})
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	promptAtomicCapabilityClient(t, ctx, client, session, "close-race-warm", "BASIC")
	waitAtomicAuditCount(t, audit, "documents:start:", 1)
	configuration, err := client.ApplicationConfiguration(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		missing := []string{filepath.Join(root, "missing-skill-root")}
		_, updateErr := client.UpdateApplicationConfiguration(ctx, session, application.UpdateConfigurationRequest{
			OperationID: "close-race-invalid", ExpectedConfigurationRevision: configuration.Revision,
			Patch: application.ConfigurationPatch{SkillRoots: &missing},
		})
		finished <- updateErr
	}()
	<-started
	// Close the SSE stream before the test server, which waits for active clients.
	_ = feed.Subscription.Close()
	host.close(t)
	if updateErr := <-finished; updateErr == nil {
		t.Fatal("invalid concurrent update succeeded")
	}
	waitAtomicAuditCount(t, audit, "documents:stop:", 1)
	atomicAssertLiveService(t, audit, "documents", 0)
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
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
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
	waitAtomicApplicationMCPRunning(t, ctx, client, created.SessionID, "documents")
	waitAtomicApplicationMCPRunning(t, ctx, client, created.SessionID, "utilities")
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
	waitAtomicApplicationMCPRunning(t, ctx, client, created.SessionID, "documents")
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
	if err := feed.Subscription.Close(); err != nil {
		t.Fatal(err)
	}
	host.close(t)
	host = nil
	recoveredHost := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer recoveredHost.close(t)
	credential, err := os.ReadFile(filepath.Join(root, "app.credential"))
	if err != nil {
		t.Fatal(err)
	}
	recovered := recoveredHost.app(string(credential))
	recoveredFeed, err := recovered.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredFeed.Subscription.Close()
	if got := strings.Count(atomicAudit(audit), "documents:call"); got != beforeRecoveryCalls {
		t.Fatalf("reconnect replayed an MCP effect: before=%d after=%d", beforeRecoveryCalls, got)
	}
	recoveryStatus, err := recovered.ApplicationMCPStatus(ctx, created.SessionID)
	if err != nil || recoveryStatus.ConfigurationRevision != "3" {
		t.Fatalf("recovered desired configuration = %+v, %v", recoveryStatus, err)
	}
	promptAtomicCapabilityClient(t, ctx, recovered, created.SessionID, "atomic-recovery-warm", "BASIC")
	waitAtomicApplicationMCPRunning(t, ctx, recovered, created.SessionID, "documents")
	promptAtomicCapabilityClient(t, ctx, recovered, created.SessionID, "atomic-recovery-doc", "DOC")
	if got := strings.Count(atomicAudit(audit), "documents:call"); got != beforeRecoveryCalls+1 {
		t.Fatalf("recovered Session made %d document calls, want %d", got, beforeRecoveryCalls+1)
	}
}

func TestApplicationFirstToolSearchUsesConsistentActivationModelCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	audit := filepath.Join(root, "audit")
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	t.Setenv("CAELIS_ATOMIC_MCP_AUDIT", audit)
	server := application.MCPServer{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestAtomicDocumentsMCPHelper$"}, WorkDir: root}
	host, client, sessionID := startAtomicLifecycleSession(t, ctx, root, &atomicCapabilityProvider{}, []application.MCPServer{server})
	defer host.close(t)
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	promptAtomicCapabilityClient(t, ctx, client, sessionID, "activate-before-binding", "BASIC")
	waitAtomicApplicationMCPRunning(t, ctx, client, sessionID, "documents")

	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{
		OperationID: "late-toolsearch-model", ExpectedRevision: &status.Configuration.Revision,
	}, Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "gpt-4.2", BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"}})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect late model = %+v, %v", connected, err)
	}
	bindings, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	profileID := ""
	for _, target := range bindings.Targets {
		if target.Backend.Provider != nil && strings.HasSuffix(target.Backend.Provider.ModelConfigID, "/gpt-4.2") {
			profileID = target.ID
		}
	}
	if profileID == "" {
		t.Fatalf("late model missing from binding targets: %+v", bindings.Targets)
	}
	bound, err := host.host.BindAgentBinding(ctx, appserver.BindAgentBindingRequest{WriteBase: appserver.WriteBase{
		OperationID: "late-toolsearch-binding", ExpectedRevision: &connected.Revision,
	}, Binding: agentbinding.Binding{Handle: agentbinding.HandleToolSearch, ProfileID: profileID, Effort: "none"}})
	if err != nil || bound.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("bind late ToolSearch model = %+v, %v", bound, err)
	}

	promptAtomicCapabilityClient(t, ctx, client, sessionID, "first-search-after-binding", "DOC")
	if got := strings.Count(atomicAudit(audit), "documents:call:"); got != 1 {
		t.Fatalf("first search after Host binding made %d document calls, want 1; audit: %s", got, atomicAudit(audit))
	}
}

func waitAtomicApplicationMCPRunning(t *testing.T, ctx context.Context, client *httpclient.Client, session, name string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := client.ApplicationMCPStatus(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		for _, server := range status.Servers {
			if server.Name != name {
				continue
			}
			if server.Status == "running" {
				return
			}
			if server.Status == "failed" {
				t.Fatalf("MCP service %q failed to start: %+v", name, server)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("MCP service %q did not become ready: %+v: %v", name, status.Servers, ctx.Err())
		case <-ticker.C:
		}
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
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
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
			if server.Name == "failed" && (len(server.Tools) != 0 || len(server.ToolDetails) != 0) {
				t.Fatalf("failed service exposed tool details: %+v", server)
			}
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
	promptAtomicCapabilityClientWithMCPApproval(t, ctx, client, session, operation, input, "allow_once")
}

// promptAtomicCapabilityClientWithMCPApproval resolves only the synthetic MCP
// fixture's real pending approval. An empty option asserts no approval is due.
func promptAtomicCapabilityClientWithMCPApproval(t *testing.T, ctx context.Context, client *httpclient.Client, session, operation, input, option string) int {
	t.Helper()
	result, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
		PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: session, OperationID: operation}, Input: input}, SourceKind: "user",
	})
	if err != nil || (result.Outcome != appserver.OutcomeAccepted && result.Outcome != appserver.OutcomeCommitted) {
		t.Fatalf("prompt %s = %+v, %v", operation, result, err)
	}
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	resolved := 0
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: session})
		if err != nil {
			t.Fatal(err)
		}
		if active := state.Approval.Active; active != nil {
			if option == "" || active.Permission == nil || !strings.Contains(active.Permission.ToolCall.Name, "__") {
				t.Fatalf("unexpected pending approval: %+v", active)
			}
			found := false
			for _, offered := range active.Permission.Options {
				found = found || offered.ID == option
			}
			if !found || len(active.Permission.Options) != 4 {
				t.Fatalf("MCP option %q missing from %+v", option, active.Permission.Options)
			}
			resolved++
			_, err := client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
				WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("%s-mcp-approval-%d", operation, resolved), SessionID: session},
				Target:    active.Target, ApprovalRequestID: string(active.RequestID), Outcome: "selected", OptionID: option, Approved: option != "cancel",
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if !state.Run.Active {
			return resolved
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for Session %s completion: %v", session, ctx.Err())
		case <-ticker.C:
		}
	}
}
