package gatewayapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
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

func TestApplicationProfileChildHTTPIsolationAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	store := filepath.Join(root, "store")
	model := &applicationHTTPModel{}
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
	if bound.Profile.Instructions != profile.Instructions || bound.Profile.Execution != profile.Execution || len(bound.Profile.Tools) != 1 || len(bound.Profile.MCPServers) != 1 || len(bound.Profile.SkillRoots) != 1 {
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
	var call sessionprofile.Call
	for {
		calls, err := client.SessionToolCalls(ctx, child)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) > 0 {
			call = calls[0]
			break
		}
		select {
		case <-ctx.Done():
			state, _ := client.InspectSession(context.WithoutCancel(ctx), appserver.StateRequest{SessionID: child})
			parent, _ := client.InspectSession(context.WithoutCancel(ctx), appserver.StateRequest{SessionID: created.SessionID})
			t.Fatalf("child callback missing: child=%+v parent=%+v model=%d", state, parent, model.requestCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if call.SessionID != child || call.Source != bound.Child.Source || call.TurnID == "" || call.ItemID == "" {
		t.Fatalf("child callback provenance: %+v", call)
	}
	// A callback can arrive while the independently initialized MCP service
	// is still connecting. Hold its startup to make this ordering deterministic.
	health, err := client.SessionMCPStatus(ctx, child)
	if err != nil || health.SessionID != child || health.ConfigurationRevision == "" || len(health.Servers) != 1 || health.Servers[0].Name != "documents" || health.Servers[0].Status != "connecting" || len(health.Skills) != 1 || health.Skills[0].Name != "profile-child-skill" || health.Skills[0].Status != "ready" {
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
		if health.Servers[0].Status == "running" {
			break
		}
		if health.Servers[0].Status != "connecting" {
			t.Fatalf("child MCP initialization failed: %+v", health)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("child MCP did not finish initialization: %+v", health)
		case <-time.After(10 * time.Millisecond):
		}
	}
	server := health.Servers[0]
	if !slices.Equal(server.Tools, []string{"lookup"}) || len(server.ToolDetails) != 1 || server.ToolDetails[0].Name != "lookup" || !strings.Contains(server.ToolDetails[0].Description, "Look up a synthetic documents value") || len(health.Skills) != 1 || health.Skills[0].Name != "profile-child-skill" || health.Skills[0].Status != "ready" {
		t.Fatalf("child MCP/Skill assembly: %+v", health)
	}
	if _, err = client.SessionToolCall(ctx, created.SessionID, call.ID); err == nil {
		t.Fatal("parent addressed child callback as its own")
	}
	if _, err = foreign.SessionToolCall(ctx, child, call.ID); err == nil {
		t.Fatal("foreign connection addressed child callback")
	}
	claimed, err := client.ClaimSessionToolCall(ctx, child, call.ID)
	if err != nil || claimed.SessionID != child {
		t.Fatalf("claim child callback: %+v %v", claimed, err)
	}
	if err = client.CompleteSessionToolCall(ctx, child, call.ID, sessionprofile.CallResult{Outcome: "succeeded", Content: json.RawMessage(`{"value":"child-complete"}`)}); err != nil {
		t.Fatal(err)
	}
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
	model.mu.Unlock()
	if !strings.Contains(raw, "child-only-input") || !strings.Contains(raw, profile.Instructions) || !strings.Contains(raw, "profile-child-skill") || strings.Contains(raw, "RunCommand") || strings.Contains(raw, "StartThread") {
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

	retained, err := client.SessionToolCall(ctx, child, call.ID)
	if err != nil || retained.State != "completed" || retained.Source != call.Source {
		t.Fatalf("retained callback receipt: %+v %v", retained, err)
	}
}

// TestApplicationChildMCPStartupHelper keeps initialization pending until the
// parent observes its independent callback and explicitly releases this fixture.
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
