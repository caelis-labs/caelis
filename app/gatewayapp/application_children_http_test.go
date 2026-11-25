package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func applicationProfileChild(t *testing.T, ctx context.Context, client *httpclient.Client, root string) (string, string) {
	t.Helper()
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "profile-child", SessionID: root, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Handle: "self", Label: "child", Input: "child-only-input", Background: true})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil {
		t.Fatalf("start profile child: %+v, %v", result, err)
	}
	for {
		state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: root})
		if err != nil {
			t.Fatal(err)
		}
		for _, participant := range state.Participants {
			if participant.DelegationID == result.Resource.Ref && participant.SessionID != "" {
				return participant.SessionID, result.Resource.Ref
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("child canonical binding missing: %+v", state)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func connectApplicationChildModel(t *testing.T, ctx context.Context, host *applicationHTTPHost, model string) {
	t.Helper()
	connectApplicationChildModelWithImage(t, ctx, host, model, nil)
}

func connectApplicationChildModelWithImage(t *testing.T, ctx context.Context, host *applicationHTTPHost, model string, image *bool) {
	t.Helper()
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{WriteBase: appserver.WriteBase{OperationID: "connect-child-" + model, ExpectedRevision: &status.Configuration.Revision}, Config: appserver.ConnectConfig{Provider: "custom-chat", Model: model, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY", ImageInput: image}})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect child model: %+v %v", result, err)
	}
}

type retryingApplicationChildProvider struct {
	calls atomic.Int32
}

func (p *retryingApplicationChildProvider) RoundTrip(req *http.Request) (*http.Response, error) {
	defer req.Body.Close()
	if p.calls.Add(1) == 1 {
		const partialStream = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Retired child answer.\"}}]}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(partialStream)), Request: req}, nil
	}
	return reviewerHTTPStream(req, map[string]any{"role": "assistant"}, "stop")
}

func TestApplicationProfileChildRetiresAttemptAcrossDraftACP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &retryingApplicationChildProvider{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	parent := createApplicationHTTPSession(t, ctx, client, "parent")
	_, taskID := applicationProfileChild(t, ctx, client, parent)
	tasks, err := httpclient.NewTaskClient(client)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := tasks.Subscribe(ctx, taskstream.SubscribeRequest{SessionID: parent, TaskID: taskID, Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Subscription.Close()
	var partialID string
	cleared := false
	for delivery := range stream.Subscription.Deliveries() {
		for _, event := range delivery.Events {
			if chunk, ok := event.Update.(eventstream.ContentChunk); ok && chunk.SessionUpdate == eventstream.UpdateAgentMessage {
				switch text := session.ExtractProtocolText(chunk.Content); {
				case text == "Retired child answer.":
					partialID = chunk.MessageID
				case text == "" && chunk.Replacement && partialID != "" && chunk.MessageID == partialID:
					cleared = true
				}
			}
			if event.Lifecycle != nil && eventstream.IsTerminalLifecycleState(event.Lifecycle.State) {
				if event.Lifecycle.State != eventstream.LifecycleStateCompleted || !cleared || provider.calls.Load() != 2 {
					t.Fatalf("child terminal = %q, partial = %q, cleared = %t, requests = %d", event.Lifecycle.State, partialID, cleared, provider.calls.Load())
				}
				return
			}
		}
	}
	t.Fatalf("child Task stream ended before completion: %v", stream.Subscription.Err())
}

func TestApplicationProfileChildHTTPIsolationAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	store := filepath.Join(root, "store")
	releaseModel := make(chan struct{})
	model := &applicationHTTPModel{entered: make(chan struct{}), release: releaseModel}
	host := startApplicationHTTPHost(t, store, root, model)
	defer func() {
		if host != nil {
			host.close(t)
		}
	}()
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, connection := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	info, err := client.Initialize(ctx)
	if err != nil || !slices.Contains(info.Capabilities, appserver.CapabilitySessionProfileChildren) {
		t.Fatalf("profile child capability missing: %+v %v", info, err)
	}
	foreign, _ := connectSessionOwnerHTTP(t, ctx, host, "foreign-owner", filepath.Join(root, "foreign.token"))
	profile := applicationHTTPProfile()
	selectedSkill := filepath.Join(root, "selected-skill")
	writeAtomicSkill(t, selectedSkill, "profile-child-skill", "PROFILE_CHILD_SKILL_BODY")
	profile.SkillRoots = []string{selectedSkill}
	t.Setenv("CAELIS_ATOMIC_MCP_HELPER", "1")
	startupRelease := filepath.Join(root, "child-mcp-startup-release")
	t.Setenv("CAELIS_APPLICATION_CHILD_MCP_START_RELEASE", startupRelease)
	defer func() {
		if err := os.WriteFile(startupRelease, nil, 0o600); err != nil {
			t.Error(err)
		}
	}()
	profile.MCPServers = []sessionprofile.MCPServer{{Name: "documents", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestApplicationChildMCPStartupHelper$"}, WorkDir: root}}
	created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "parent"}, Profile: profilePointer(profile)})
	if err != nil {
		t.Fatal(err)
	}
	handles, err := client.Handles(ctx, created.SessionID)
	if err != nil || !slices.Contains(handles, "self") {
		t.Fatalf("profile handles: %v %v", handles, err)
	}
	child, task := applicationProfileChild(t, ctx, client, created.SessionID)
	if child == created.SessionID {
		t.Fatal("child reused parent canonical Session")
	}
	bound, err := client.SessionProfile(ctx, child)
	if err != nil || bound.Child == nil || bound.Child.ParentSessionID != created.SessionID || bound.Child.RootSessionID != created.SessionID || bound.Child.ParentTaskID != task || bound.PrincipalID != connection.PrincipalID || bound.Child.Source != (sessionprofile.Source{Kind: "user", OperationID: "profile-child"}) {
		t.Fatalf("durable child relation: %+v %v", bound, err)
	}
	if bound.Profile.Instructions != profile.Instructions || bound.Profile.Execution != profile.Execution || len(bound.Profile.Tools) != 0 || len(bound.Profile.MCPServers) != 1 || len(bound.Profile.SkillRoots) != 1 {
		t.Fatalf("profile inherited incorrectly: %+v", bound.Profile)
	}
	childBytes := []byte("CHILD_RESOURCE_BYTES")
	digest := sha256.Sum256(childBytes)
	resource, err := resourceResult(client.CreateSessionResource(ctx, appserver.SessionResourceRequest{WriteBase: appserver.WriteBase{OperationID: "child-resource", SessionID: child}, Name: "child-data", MediaType: "application/octet-stream", Data: childBytes, SHA256: hex.EncodeToString(digest[:])}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		who *httpclient.Client
		sid string
	}{{client, created.SessionID}, {foreign, child}} {
		if _, err := test.who.SessionResourceContent(ctx, test.sid, resource.ID); err == nil {
			t.Fatal("child resource escaped its exact Session/scope")
		}
	}
	read, err := client.SessionResourceContent(ctx, child, resource.ID)
	if err != nil || string(read.Data) != string(childBytes) {
		t.Fatalf("child resource owner: %+v %v", read, err)
	}

	if _, err = foreign.InspectSession(ctx, appserver.StateRequest{SessionID: child}); errorcode.CodeOf(err) != errorcode.PermissionDenied && errorcode.CodeOf(err) != errorcode.NotFound {
		t.Fatalf("foreign child observation: %v", err)
	}
	if _, err = client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "nested", SessionID: child}, Handle: "self", Input: "nested", Background: true}); err == nil {
		t.Fatal("child acquired nested delegation")
	}
	select {
	case <-model.entered:
	case <-ctx.Done():
		t.Fatal("child provider did not start")
	}
	health, err := client.SessionMCPStatus(ctx, child)
	childSkillReady := func(status sessionprofile.MCPStatus) bool {
		return slices.ContainsFunc(status.Skills, func(item sessionprofile.SkillStatus) bool {
			return item.Name == "profile-child-skill" && item.Status == "ready"
		})
	}
	if err != nil || health.SessionID != child || health.ConfigurationRevision == "" || len(health.Servers) != 1 || health.Servers[0].Name != "documents" || health.Servers[0].Status != "connecting" || !childSkillReady(health) {
		t.Fatalf("held child MCP/Skill assembly: %+v %v", health, err)
	}
	healthRevision := health.ConfigurationRevision
	if err := os.WriteFile(startupRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		health, err = client.SessionMCPStatus(ctx, child)
		if err != nil || health.SessionID != child || health.ConfigurationRevision != healthRevision || len(health.Servers) != 1 || health.Servers[0].Name != "documents" {
			t.Fatalf("child MCP health lost its exact owner: %+v %v", health, err)
		}
		if health.Servers[0].Status == "running" && !health.Servers[0].Refreshing && !health.Servers[0].Stale {
			break
		}
		if health.Servers[0].Status != "connecting" && health.Servers[0].Status != "running" {
			t.Fatalf("child MCP initialization failed: %+v", health)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("child MCP did not finish initialization: %+v", health)
		case <-time.After(10 * time.Millisecond):
		}
	}
	server := health.Servers[0]
	if !slices.Equal(server.Tools, []string{"lookup"}) || len(server.ToolDetails) != 1 || server.ToolDetails[0].Name != "lookup" || !strings.Contains(server.ToolDetails[0].Description, "Look up a synthetic documents value") || !childSkillReady(health) {
		t.Fatalf("child MCP/Skill assembly: %+v", health)
	}
	close(releaseModel)
	waitApplicationHTTPIdle(t, ctx, client, child)
	childState, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: child})
	if err != nil {
		t.Fatal(err)
	}
	parentState, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if childState.Revision == 0 || childState.Controller.Source != "acp_ingress" || parentState.Run.Active {
		t.Fatalf("independent child/parent lifecycle: %+v %+v", childState, parentState)
	}
	model.mu.Lock()
	raw := strings.Join(func() []string {
		out := make([]string, len(model.requests))
		for i, r := range model.requests {
			out[i] = string(r)
		}
		return out
	}(), "\n")
	for _, encoded := range model.requests {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(encoded, &request); err != nil {
			model.mu.Unlock()
			t.Fatal(err)
		}
		for _, declared := range request.Tools {
			if declared.Function.Name == "RunCommand" || declared.Function.Name == "StartThread" {
				model.mu.Unlock()
				t.Fatalf("child imported ambient/nested declaration: %s", declared.Function.Name)
			}
		}
	}
	model.mu.Unlock()
	if !strings.Contains(raw, "child-only-input") || !strings.Contains(raw, profile.Instructions) || !strings.Contains(raw, "profile-child-skill") {
		t.Fatalf("child imported ambient/nested tools: %s", raw)
	}
	host.close(t)
	host = nil
	host = startApplicationHTTPHost(t, store, root, model)
	token, err := os.ReadFile(filepath.Join(root, "owner.token"))
	if err != nil {
		t.Fatal(err)
	}
	client = host.app(strings.TrimSpace(string(token)))
	recovered, err := client.SessionProfile(ctx, child)
	if err != nil || recovered.Child == nil || *recovered.Child != *bound.Child {
		t.Fatalf("recovered relation: %+v %v", recovered, err)
	}
	reconnect, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: child})
	if err != nil {
		t.Fatal(err)
	}
	defer reconnect.Subscription.Close()
	if reconnect.State.SessionID != child || reconnect.State.Run.Active {
		t.Fatalf("recovered child state: %+v", reconnect.State)
	}
	retainedResource, err := client.SessionResourceContent(ctx, child, resource.ID)
	if err != nil || retainedResource.Resource.ID != resource.ID || string(retainedResource.Data) != string(childBytes) {
		t.Fatalf("resource recovery lost exact binding: %+v %v", retainedResource, err)
	}

}

// TestApplicationChildMCPStartupHelper keeps initialization pending until the
// parent observes the independent model request and explicitly releases this fixture.
func TestApplicationChildMCPStartupHelper(t *testing.T) {
	if os.Getenv("CAELIS_ATOMIC_MCP_HELPER") != "1" {
		return
	}
	release := os.Getenv("CAELIS_APPLICATION_CHILD_MCP_START_RELEASE")
	if release == "" {
		t.Fatal("MCP startup release path missing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for {
		if _, err := os.Stat(release); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("MCP startup fixture was not released")
		case <-time.After(10 * time.Millisecond):
		}
	}
	runAtomicMCPHelper(t, "documents")
}
