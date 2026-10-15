package gatewayapp_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// Exercise production HTTP, provider, SDK tool wrappers, Task and scoped native
// ACP endpoint. No mock participant or Session command backend is involved.
type applicationChildrenModel struct {
	mu       sync.Mutex
	requests [][]byte
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (p *applicationChildrenModel) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	p.mu.Lock()
	p.requests = append(p.requests, append([]byte(nil), raw...))
	p.mu.Unlock()
	var payload struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if len(payload.Messages) > 0 && payload.Messages[len(payload.Messages)-1].Role == "tool" {
		return reviewerHTTPStream(req, map[string]any{"role": "assistant", "content": "PROFILE_CHILD_DONE"}, "stop")
	}
	name, input := "ApplicationLookup", `{"key":"child"}`
	if bytes.Contains(raw, []byte("SPAWN_PROFILE_CHILD")) && !bytes.Contains(raw, []byte("CHILD_PROFILE_INPUT")) {
		name, input = "StartThread", `{"agent":"self","handle":"leaf","prompt":"CHILD_PROFILE_INPUT"}`
	}
	if bytes.Contains(raw, []byte("DELAYED_PROFILE_MAIL")) {
		p.once.Do(func() { close(p.reached) })
		select {
		case <-p.release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		name, input = "SendMessage", `{"to":"child","message":"MUST_NOT_BE_COMMITTED"}`
	}
	return reviewerHTTPStream(req, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "profile-native-call", "index": 0, "type": "function", "function": map[string]any{"name": name, "arguments": input}}}}, "tool_calls")
}
func applicationAttachedChild(t *testing.T, ctx context.Context, client *httpclient.Client, root, label string) (string, string) {
	t.Helper()
	for {
		state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: root})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range state.Participants {
			if strings.TrimPrefix(p.Label, "@") == label && p.SessionID != "" {
				return p.SessionID, p.DelegationID
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native child not attached: %+v", state)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func TestApplicationProfileChildModelSpawnKeepsBackgroundSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &applicationChildrenModel{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	sid := createApplicationHTTPSession(t, ctx, client, "parent")
	grant := backgroundGrant(t, ctx, client, sid, "child-grant", "approved-schedule")
	backgroundPrompt(t, ctx, client, sid, "background-spawn", "SPAWN_PROFILE_CHILD", "authorized_background", grant.ID)
	child, task := applicationAttachedChild(t, ctx, client, sid, "leaf")
	binding, err := client.SessionProfile(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	want := sessionprofile.Source{Kind: "authorized_background", OperationID: "background-spawn", GrantID: grant.ID, AuthorizedSource: grant.Source}
	if binding.Child == nil || binding.Child.Source != want || binding.Child.ParentTaskID != task {
		t.Fatalf("source lost: %+v", binding)
	}
	call := backgroundCall(t, ctx, client, child)
	if call.Source != want || call.SessionID != child {
		t.Fatalf("callback source relabeled: %+v", call)
	}
	completeBackgroundCall(t, ctx, client, call)
	waitApplicationHTTPIdle(t, ctx, client, sid)
	tasks, err := httpclient.NewTaskClient(client)
	if err != nil {
		t.Fatal(err)
	}
	for {
		result, err := tasks.List(ctx, taskstream.ListRequest{SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
		terminal := false
		for _, entry := range result.Tasks {
			if entry.TaskID == task && !entry.Running {
				terminal = true
			}
		}
		if terminal {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native Task did not settle: %+v", result)
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, req := range []appserver.PromptRequest{
		{WriteBase: appserver.WriteBase{OperationID: "relabel-child", SessionID: child}, Input: "forge user", SourceKind: "user"},
		{WriteBase: appserver.WriteBase{OperationID: "wrong-grant", SessionID: child}, Input: "forge background", SourceKind: "authorized_background", GrantID: "unknown"},
	} {
		if _, err := client.Prompt(ctx, req); err == nil {
			t.Fatal("child source was widened")
		}
	}
	if _, err := grantResult(client.RevokeSessionBackgroundGrant(ctx, revokeGrantCommand(sid, grant.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prompt(ctx, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "revoked-child-source", SessionID: child}, Input: "must not run", SourceKind: "authorized_background", GrantID: grant.ID}); err == nil {
		t.Fatal("child retained revoked root background grant")
	}
}
func TestSessionClosureCancelsProviderBeforeCollaborationEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &applicationChildrenModel{reached: make(chan struct{}), release: make(chan struct{})}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, connection := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	sid := createApplicationHTTPSession(t, ctx, client, "parent")
	child, _ := applicationProfileChild(t, ctx, client, sid)
	completeBackgroundCall(t, ctx, client, backgroundCall(t, ctx, client, child))
	backgroundPrompt(t, ctx, client, sid, "delayed-mail", "DELAYED_PROFILE_MAIL", "user", "")
	select {
	case <-provider.reached:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	principal := appserver.Principal{ID: connection.PrincipalID}
	lease, err := host.stack.ControlRuntimes().Acquire(ctx, principal, appserver.ActionSessionInspect, sid, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close(context.WithoutCancel(ctx))

	if err := closeProfileTestSession(ctx, client, sid); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	// The original native Run stops under canonical Session closure.
	for len(lease.KernelReads().TurnState().ActiveTurns()) > 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}

	zero := uint64(0)
	if _, err := host.stack.CollaborationService().ReadMessages(ctx, collaboration.Identity{Session: sid, Member: "parent"}, &zero, 128); !errors.Is(err, collaboration.ErrSessionClosed) {
		t.Fatalf("closed Session collaboration read = %v", err)
	}
	// Closure purges the shared log. Inspect storage independently after the
	// delayed provider has stopped: no callback may recreate a closed mailbox.
	db, err := sql.Open("sqlite", filepath.Join(root, "store", "control", "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"collaboration_messages", "collaboration_mailbox"} {
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE session=?", sid).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("closed Session %s rows = %d, %v", table, rows, err)
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	for _, raw := range provider.requests {
		if bytes.Contains(raw, []byte("MUST_NOT_BE_COMMITTED")) && bytes.Contains(raw, []byte("CHILD_PROFILE_INPUT")) {
			t.Fatal("closed Session mail reached target model")
		}
	}
}

func TestApplicationProfileChildApprovalKeepsNativeOwnerAndOptions(t *testing.T) {
	for _, test := range []struct {
		kind     string
		approved bool
	}{
		{"allow_once", false},
		{"reject_once", true},
	} {
		t.Run(test.kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &applicationHTTPModel{})
			defer host.close(t)
			connectApplicationChildModel(t, ctx, host, "gpt-4.1")
			client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
			foreign, _ := connectSessionOwnerHTTP(t, ctx, host, "foreign-owner", filepath.Join(root, "foreign.token"))
			profile := applicationHTTPProfile()
			profile.Permissions.ApprovalMode = "manual"
			profile.Tools[0].ApprovalPolicy = "required"
			created, err := client.CreateSession(ctx, appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "approval-parent"}, Profile: profilePointer(profile)})
			if err != nil {
				t.Fatal(err)
			}
			child, task := applicationProfileChild(t, ctx, client, created.SessionID)
			var pending *appserver.ActiveApproval
			for pending == nil {
				state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
				if err != nil {
					t.Fatal(err)
				}
				pending = state.Approval.Active
				if pending == nil {
					select {
					case <-ctx.Done():
						t.Fatal("native child approval missing")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			if pending.Permission == nil || len(pending.Permission.Options) == 0 || pending.ScopeID != task || pending.Target.TurnID == "" {
				t.Fatalf("child approval native owner lost: %+v", pending)
			}
			calls, err := client.SessionToolCalls(ctx, child)
			if err != nil || len(calls) != 0 {
				t.Fatalf("callback preceded approval: %+v %v", calls, err)
			}
			replay, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: created.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Subscription.Close()
			before, _ := json.Marshal(pending)
			after, _ := json.Marshal(replay.State.Approval.Active)
			if !bytes.Equal(before, after) {
				t.Fatalf("approval replay differs: %s %s", before, after)
			}
			option := ""
			for _, o := range pending.Permission.Options {
				if o.Kind == test.kind {
					option = o.ID
				}
			}
			if option == "" {
				t.Fatalf("native %s option missing: %+v", test.kind, pending.Permission.Options)
			}
			// The original native option owns the outcome. This contrary boolean must
			// neither suppress a valid grant nor turn a rejection into a grant.
			request := appserver.ResolveApprovalRequest{WriteBase: appserver.WriteBase{OperationID: "approve-child", SessionID: created.SessionID}, Target: pending.Target, ApprovalRequestID: string(pending.RequestID), Outcome: "selected", OptionID: option, Approved: test.approved}
			if _, err := foreign.ResolveApproval(ctx, request); err == nil {
				t.Fatal("foreign connection resolved native approval")
			}
			bad := request
			bad.OperationID = "bad-child-option"
			bad.OptionID = "forged-option"
			if _, err := client.ResolveApproval(ctx, bad); err == nil {
				t.Fatal("invented approval option accepted")
			}
			if _, err := client.ResolveApproval(ctx, request); err != nil {
				t.Fatal(err)
			}
			if test.kind == "reject_once" {
				waitApplicationHTTPIdle(t, ctx, client, child)
				calls, err := client.SessionToolCalls(ctx, child)
				if err != nil || len(calls) != 0 {
					t.Fatalf("native rejection executed callback: %+v %v", calls, err)
				}
				return
			}
			call := backgroundCall(t, ctx, client, child)
			if call.Source != (sessionprofile.Source{Kind: "user", OperationID: "profile-child"}) {
				t.Fatalf("approval relabeled source: %+v", call)
			}
			completeBackgroundCall(t, ctx, client, call)
		})
	}
}

func TestApplicationProfileChildInheritsAutomaticReviewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &reviewerHTTPProvider{toolName: "ApplicationLookup", args: `{"key":"one"}`, decision: `{"option_id":"allow_once"}`}
	host, client, sid := setupReviewerHTTP(t, ctx, root, provider)
	defer host.close(t)
	child, _ := applicationProfileChild(t, ctx, client, sid)
	binding, err := client.SessionProfile(ctx, child)
	if err != nil || binding.Profile.Reviewer == nil || binding.Profile.Reviewer.Model != "custom/reviewer-model" {
		t.Fatalf("reviewer inheritance: %+v %v", binding, err)
	}
	call := backgroundCall(t, ctx, client, child)
	completeBackgroundCall(t, ctx, client, call)
	reviewed := false
	for _, request := range provider.snapshot() {
		if request["model"] == "reviewer-model" {
			reviewed = true
		}
	}
	if !reviewed {
		t.Fatal("child bypassed inherited native Guardian reviewer")
	}
}

func TestApplicationProfileForegroundParticipantHasIndependentSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &applicationHTTPModel{})
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	sid := createApplicationHTTPSession(t, ctx, client, "parent")
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "direct-profile-child", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Handle: "self", Label: "direct", Input: "child-only-input"})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("direct native participant: %+v %v", result, err)
	}
	child, _ := applicationAttachedChild(t, ctx, client, sid, "direct")
	binding, err := client.SessionProfile(ctx, child)
	if err != nil || binding.Child == nil || binding.Child.ParentSessionID != sid || binding.Child.ParentTaskID != "" || child == sid {
		t.Fatalf("direct relation: %+v %v", binding, err)
	}
	call := backgroundCall(t, ctx, client, child)
	completeBackgroundCall(t, ctx, client, call)
	grandchild, grandTask := applicationProfileChild(t, ctx, client, child)
	grandBinding, err := client.SessionProfile(ctx, grandchild)
	if err != nil || grandBinding.Child == nil || grandBinding.Child.RootSessionID != sid || grandBinding.Child.ParentSessionID != child || grandBinding.Child.ParentTaskID != grandTask {
		t.Fatalf("recursive native relation: %+v %v", grandBinding, err)
	}
	completeBackgroundCall(t, ctx, client, backgroundCall(t, ctx, client, grandchild))

	taskClient, err := httpclient.NewTaskClient(client)
	if err != nil {
		t.Fatal(err)
	}
	for {
		tasks, err := taskClient.List(ctx, taskstream.ListRequest{SessionID: child})
		if err != nil {
			t.Fatal(err)
		}
		done := false
		for _, task := range tasks.Tasks {
			if task.TaskID == grandTask && !task.Running {
				done = true
			}
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// A finished grandchild Task does not mean its parent Turn has finished
	// consuming the result. Observe the child Turn's own completion before
	// testing closure; history CAS races are covered separately.
	childState := waitApplicationHTTPIdle(t, ctx, client, child)
	if _, err := client.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{OperationID: "close-direct-child", SessionID: child, ExpectedControllerEpoch: childState.Controller.EpochID}}); err != nil {
		t.Fatal(err)
	}
	closed, err := appserver.IsSessionClosed(ctx, host.stack.Sessions(), session.SessionRef{SessionID: sid})
	if err != nil || closed {
		t.Fatalf("child closure affected parent: %v %v", closed, err)
	}
}

func TestApplicationProfileNamedNativeModelKeepsProfileAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	provider := &applicationHTTPModel{}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, provider)
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "gpt-4.1")
	connectApplicationChildModel(t, ctx, host, "gpt-4.2")
	bindings, err := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	target, effort := "", ""
	for _, profile := range bindings.Targets {
		if profile.Backend.Provider != nil && strings.HasSuffix(profile.Backend.Provider.ModelConfigID, "/gpt-4.2") {
			target = profile.ID
			effort = profile.Effort.DefaultEffort
		}
	}
	if target == "" {
		t.Fatalf("registered provider target missing: %+v", bindings.Targets)
	}
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := host.host.CreateAgentRole(ctx, appserver.CreateAgentRoleRequest{WriteBase: appserver.WriteBase{OperationID: "named-child-role", ExpectedRevision: &status.Configuration.Revision}, Role: agentbinding.Role{Handle: "research", Description: "Named native model child"}, Binding: agentbinding.Binding{ProfileID: target, Effort: effort}})
	if err != nil || configured.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("native role: %+v %v", configured, err)
	}
	client, _ := connectSessionOwnerHTTP(t, ctx, host, "owner", filepath.Join(root, "owner.token"))
	sid := createApplicationHTTPSession(t, ctx, client, "parent")
	handles, err := client.Handles(ctx, sid)
	if err != nil || !strings.Contains(strings.Join(handles, ","), "research") {
		t.Fatalf("named model missing: %v %v", handles, err)
	}
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "native-model-child", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Handle: "research", Label: "research", Input: "child-only-input", Background: true})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("named profile child: %+v %v", result, err)
	}
	child, _ := applicationAttachedChild(t, ctx, client, sid, "research")
	completeBackgroundCall(t, ctx, client, backgroundCall(t, ctx, client, child))
	configuration, err := client.SessionConfiguration(ctx, child)
	if err != nil || !strings.HasSuffix(configuration.Profile.Model, "/gpt-4.2") || configuration.Profile.Instructions != applicationHTTPProfile().Instructions || len(configuration.Profile.Tools) != 1 {
		t.Fatalf("native model override expanded profile: %+v %v", configuration, err)
	}
	parent, err := client.SessionConfiguration(ctx, sid)
	if err != nil || parent.Profile.Model != "custom/gpt-4.1" {
		t.Fatalf("child model rewrote parent: %+v %v", parent, err)
	}
	state, err = client.InspectSession(ctx, appserver.StateRequest{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := client.StartParticipant(ctx, appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{OperationID: "unknown-profile-role", SessionID: sid, ExpectedRevision: &state.Revision, ExpectedControllerEpoch: state.Controller.EpochID}, Handle: "unknown-external", Label: "must-not-launch", Input: "not admitted", Background: true})
	if err == nil || rejected.Resource != nil || rejected.Target.TurnID != "" {
		t.Fatalf("unowned endpoint admitted effect: %+v %v", rejected, err)
	}
}
