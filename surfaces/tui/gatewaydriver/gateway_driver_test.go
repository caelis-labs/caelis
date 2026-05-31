package gatewaydriver

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	coreconfig "github.com/OnslaughtSnail/caelis/core/config"
	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/internal/testenv"
)

func encryptCodeFreeAPIKeyForRuntimeTest(t *testing.T, apiKey string) string {
	t.Helper()
	block, err := aes.NewCipher([]byte("Xtpa6sS&+D.NAo%CP8LA:7pk"))
	if err != nil {
		t.Fatalf("init aes cipher: %v", err)
	}
	blockSize := block.BlockSize()
	pad := blockSize - (len(apiKey) % blockSize)
	plain := append([]byte(apiKey), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte("%1KJIrl3!XUxr04V")).CryptBlocks(out, plain)
	return base64.StdEncoding.EncodeToString(out)
}

func ptrRuntimeMessage(message coremodel.Message) *coremodel.Message {
	return &message
}

func testCoreTextMessage(role coremodel.Role, text string) coremodel.Message {
	return coremodel.Message{Role: role, Parts: []coremodel.Part{coremodel.NewTextPart(text)}}
}

func cloneTestState(state map[string]any) map[string]any {
	next := maps.Clone(state)
	if next == nil {
		next = map[string]any{}
	}
	return next
}

func testGatewayStatusView(ref coresession.Ref, workspace coresession.Workspace, modelAlias string, modeID string) appviewmodel.StatusView {
	modelAlias = strings.TrimSpace(modelAlias)
	modeID = firstNonEmpty(strings.TrimSpace(modeID), "auto-review")
	return appviewmodel.StatusView{
		Runtime: appviewmodel.RuntimeStatus{
			AppName:        firstNonEmpty(ref.AppName, "caelis"),
			UserID:         ref.UserID,
			WorkspaceKey:   firstNonEmpty(ref.WorkspaceKey, workspace.Key),
			WorkspaceCWD:   workspace.CWD,
			DefaultModel:   modelAlias,
			SandboxBackend: "host",
		},
		Session: &appviewmodel.SessionStatus{
			Ref:       ref,
			Workspace: workspace,
			Status:    "idle",
		},
		Model: appviewmodel.ModelStatus{
			Configured: modelAlias != "",
			Current: &appviewmodel.ModelChoice{
				ID:      modelAlias,
				Alias:   modelAlias,
				Default: true,
			},
		},
		Mode: appviewmodel.ModeStatus{
			Current: appviewmodel.ModeChoice{ID: modeID, Name: modeID},
		},
	}
}

var errGatewayDriverTestActiveRunConflict = errors.New("active participant run already in progress")

type gatewayDriverTestTurn struct {
	ref    coresession.Ref
	events chan appviewmodel.SessionEventEnvelope
}

func newGatewayDriverTestTurn(ref coresession.Ref) *gatewayDriverTestTurn {
	events := make(chan appviewmodel.SessionEventEnvelope)
	close(events)
	return &gatewayDriverTestTurn{ref: coresession.NormalizeRef(ref), events: events}
}

func (t *gatewayDriverTestTurn) HandleID() string {
	return "handle"
}

func (t *gatewayDriverTestTurn) RunID() string {
	return "run"
}

func (t *gatewayDriverTestTurn) TurnID() string {
	return "turn"
}

func (t *gatewayDriverTestTurn) SessionRef() coresession.Ref {
	return t.ref
}

func (t *gatewayDriverTestTurn) SessionEvents() <-chan appviewmodel.SessionEventEnvelope {
	return t.events
}

func (t *gatewayDriverTestTurn) Submit(context.Context, coreruntime.Submission) error {
	return nil
}

func (t *gatewayDriverTestTurn) Cancel() coreruntime.CancelResult {
	return coreruntime.CancelResult{Status: coreruntime.CancelAlreadyCancelled}
}

func (t *gatewayDriverTestTurn) Close() error {
	return nil
}

func closeGatewayDriverTestTurn(t *testing.T, turn Turn) {
	t.Helper()
	if turn == nil {
		return
	}
	appTurn, ok := turn.(interface {
		SessionEvents() <-chan appviewmodel.SessionEventEnvelope
	})
	if !ok {
		t.Fatal("turn does not expose app session events")
	}
	turn.Cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-appTurn.SessionEvents():
			if !ok {
				if err := turn.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
				return
			}
		case <-timer.C:
			_ = turn.Close()
			t.Fatal("turn did not close after cancel")
		}
	}
}

func TestGatewayDriverKeepsServiceRuntimeUsableAfterSandboxUpdate(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "driver-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "driver-workspace",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Sandbox: gatewayDriverTestSandboxConfig{
			HelperPath: filepath.Join(t.TempDir(), "missing-landlock-helper"),
		},
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "rebuild-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	before, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status(before) error = %v", err)
	}
	if before.SessionID != "rebuild-session" {
		t.Fatalf("Status(before).SessionID = %q, want rebuild-session", before.SessionID)
	}
	// This test only needs to force a runtime rebuild; the missing helper keeps
	// auto landlock fallback from recursively executing this test binary in CI.
	if _, err := stack.services.Settings().SetSandboxBackend(ctx, "auto"); err != nil {
		t.Fatalf("SetSandboxBackend(auto) error = %v", err)
	}
	after, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status(after) error = %v", err)
	}
	if after.SessionID != "rebuild-session" {
		t.Fatalf("Status(after).SessionID = %q, want rebuild-session", after.SessionID)
	}
	if after.SandboxRequestedBackend == "" && after.SandboxResolvedBackend == "" {
		t.Fatalf("Status(after) sandbox = %#v, want runtime sandbox status", after)
	}
}

func TestGatewayDriverDefersBlankSessionUntilFirstSubmission(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "lazy-session-test",
		StoreDir:       storeDir,
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionID != "" {
		t.Fatalf("Status().SessionID = %q, want empty before first submission", status.SessionID)
	}
	before, err := stack.ListSessions(ctx, stack.Workspace.Key, 10)
	if err != nil {
		t.Fatalf("ListSessions(before) error = %v", err)
	}
	if len(before.Sessions) != 0 {
		t.Fatalf("ListSessions(before) = %d sessions, want none", len(before.Sessions))
	}

	turn, err := driver.Submit(ctx, Submission{Text: "hello"})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	closeGatewayDriverTestTurn(t, turn)
	after, err := stack.ListSessions(ctx, stack.Workspace.Key, 10)
	if err != nil {
		t.Fatalf("ListSessions(after) error = %v", err)
	}
	if len(after.Sessions) != 1 {
		t.Fatalf("ListSessions(after) = %d sessions, want one after first submission", len(after.Sessions))
	}
}

func TestGatewayDriverSubmitRoutesActiveSessionInputToActiveTurn(t *testing.T) {
	ctx := context.Background()
	activeSession := coresession.Session{
		Ref: coresession.Ref{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		Workspace: coresession.Workspace{Key: "ws", CWD: t.TempDir()},
	}
	active := []ActiveTurnState{{
		SessionRef: activeSession.Ref,
		Kind:       ActiveTurnKindKernel,
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
	}}
	var activeSubmits []SubmitActiveTurnRequest
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace:     activeSession.Workspace,
		ActiveTurnsFn: func() []ActiveTurnState { return active },
		StartSessionFn: func(context.Context, string, string) (coresession.Session, error) {
			return activeSession, nil
		},
		SubmitActiveTurnFn: func(_ context.Context, req SubmitActiveTurnRequest) error {
			activeSubmits = append(activeSubmits, req)
			return nil
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}

	turn, err := driver.Submit(ctx, Submission{Text: "  steer next step  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if turn != nil {
		t.Fatalf("Submit() turn = %#v, want nil for active-turn guidance", turn)
	}
	if got, want := len(activeSubmits), 1; got != want {
		t.Fatalf("active submits = %d, want %d", got, want)
	}
	if got := activeSubmits[0].Submission.Text; got != "steer next step" {
		t.Fatalf("active submit text = %q, want trimmed guidance", got)
	}
	if got := activeSubmits[0].SessionRef.SessionID; got != activeSession.Ref.SessionID {
		t.Fatalf("active submit session = %q, want %q", got, activeSession.Ref.SessionID)
	}
}

func TestGatewayDriverExecuteCommandRoutesThroughStackAndAdoptsReturnedSession(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	commandSession := coresession.Ref{
		AppName:      "caelis",
		UserID:       "user-1",
		SessionID:    "cmd-session",
		WorkspaceKey: "ws",
	}
	type commandCall struct {
		ref   coresession.Ref
		input string
		parts []coremodel.ContentPart
	}
	var calls []commandCall
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace: coresession.Workspace{Key: "ws", CWD: workspace},
		AppStatusViewFn: func(_ context.Context, ref coresession.Ref, _ bool) (appviewmodel.StatusView, error) {
			return testGatewayStatusView(ref, coresession.Workspace{Key: "ws", CWD: workspace}, "cmd-model", "auto-review"), nil
		},
		ExecuteCommandFn: func(_ context.Context, ref coresession.Ref, input string, parts []coremodel.ContentPart) (CommandExecutionView, error) {
			calls = append(calls, commandCall{
				ref:   ref,
				input: input,
				parts: append([]coremodel.ContentPart(nil), parts...),
			})
			view := CommandExecutionView{
				Handled: true,
				Command: strings.TrimPrefix(input, "/"),
				Output:  "ok",
			}
			if len(calls) == 1 {
				view.SessionRef = &commandSession
			}
			return view, nil
		},
	}, "", "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}

	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "  /new  "}); err != nil {
		t.Fatalf("ExecuteCommand(/new) error = %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(calls))
	}
	if calls[0].input != "/new" || calls[0].ref.SessionID != "" || len(calls[0].parts) != 0 {
		t.Fatalf("first command call = %#v, want trimmed /new without active session or parts", calls[0])
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionID != "cmd-session" {
		t.Fatalf("Status().SessionID = %q, want command-returned session", status.SessionID)
	}

	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/doctor"}); err != nil {
		t.Fatalf("ExecuteCommand(/doctor) error = %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("command calls = %d, want 2", len(calls))
	}
	if calls[1].input != "/doctor" || calls[1].ref.SessionID != "cmd-session" {
		t.Fatalf("second command call = %#v, want active command session", calls[1])
	}
}

func TestGatewayDriverStartupDoesNotQuerySandboxStatus(t *testing.T) {
	ctx := context.Background()
	diagnosticStatusCalls := 0
	activeSession := coresession.Session{
		Ref: coresession.Ref{
			AppName: "caelis", UserID: "user-1", SessionID: "startup-session", WorkspaceKey: "ws",
		},
		Workspace: coresession.Workspace{Key: "ws", CWD: t.TempDir()},
	}
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace: activeSession.Workspace,
		AppStatusViewFn: func(_ context.Context, ref coresession.Ref, includeDiagnostics bool) (appviewmodel.StatusView, error) {
			if includeDiagnostics {
				diagnosticStatusCalls++
			}
			view := testGatewayStatusView(ref, activeSession.Workspace, "startup-model", "auto-review")
			view.Runtime.SandboxBackend = ""
			return view, nil
		},
		StartSessionFn: func(context.Context, string, string) (coresession.Session, error) {
			return activeSession, nil
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}
	if driver.sandboxType != "auto" {
		t.Fatalf("startup sandbox type = %q, want lightweight default", driver.sandboxType)
	}
	if diagnosticStatusCalls != 0 {
		t.Fatalf("diagnostic status calls during startup = %d, want 0", diagnosticStatusCalls)
	}
}

func TestGatewayDriverLightweightStatusSkipsSandboxDiagnostics(t *testing.T) {
	ctx := context.Background()
	diagnosticStatusCalls := 0
	workspace := coresession.Workspace{Key: "ws", CWD: t.TempDir()}
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace: workspace,
		AppStatusViewFn: func(_ context.Context, ref coresession.Ref, includeDiagnostics bool) (appviewmodel.StatusView, error) {
			if includeDiagnostics {
				diagnosticStatusCalls++
			}
			return testGatewayStatusView(ref, workspace, "gpt-light", "auto-review"), nil
		},
	}, "", "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}
	status, err := driver.LightweightStatus(ctx)
	if err != nil {
		t.Fatalf("LightweightStatus() error = %v", err)
	}
	if status.Model != "gpt-light" {
		t.Fatalf("LightweightStatus().Model = %q, want default alias", status.Model)
	}
	if diagnosticStatusCalls != 0 {
		t.Fatalf("diagnostic status calls = %d, want 0", diagnosticStatusCalls)
	}
}

func TestGatewayDriverStatusCanUseSharedAppStatusView(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	activeSession := coresession.Session{
		Ref: coresession.Ref{
			AppName:      "caelis",
			UserID:       "user-1",
			SessionID:    "app-status-session",
			WorkspaceKey: "repo",
		},
		Workspace: coresession.Workspace{Key: "repo", CWD: workspace},
	}
	var seenRef coresession.Ref
	var seenDiagnostics bool
	statusCalls := 0
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace: activeSession.Workspace,
		StartSessionFn: func(context.Context, string, string) (coresession.Session, error) {
			return activeSession, nil
		},
		AppStatusViewFn: func(_ context.Context, ref coresession.Ref, includeDiagnostics bool) (appviewmodel.StatusView, error) {
			statusCalls++
			seenRef = ref
			seenDiagnostics = includeDiagnostics
			return appviewmodel.StatusView{
				Runtime: appviewmodel.RuntimeStatus{
					AppName:        "caelis",
					UserID:         "user-1",
					WorkspaceKey:   "repo",
					WorkspaceCWD:   workspace,
					StoreBackend:   "sqlite",
					StoreURI:       "/tmp/caelis-app-status.sqlite",
					SandboxBackend: "host",
				},
				Session: &appviewmodel.SessionStatus{
					Ref:       coresession.Ref{AppName: "caelis", UserID: "user-1", SessionID: "app-status-session", WorkspaceKey: "repo"},
					Workspace: coresession.Workspace{Key: "repo", CWD: workspace},
					Status:    "idle",
				},
				Model: appviewmodel.ModelStatus{
					Configured: true,
					Current: &appviewmodel.ModelChoice{
						ID:       "openai-compatible.default.gpt-test",
						Alias:    "test-model",
						Provider: "openai-compatible",
						Model:    "gpt-test",
						Default:  true,
					},
					ReasoningEffort: "high",
				},
				Mode: appviewmodel.ModeStatus{
					Current: appviewmodel.ModeChoice{ID: "manual", Name: "Manual"},
				},
				Sandbox: &appviewmodel.SandboxStatus{
					RequestedBackend: "host",
					ResolvedBackend:  "host",
					Route:            "host",
				},
			}, nil
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if statusCalls != 1 {
		t.Fatalf("AppStatusView() calls = %d, want 1", statusCalls)
	}
	if !seenDiagnostics {
		t.Fatal("AppStatusView() includeDiagnostics = false, want true for full status")
	}
	if seenRef.SessionID != activeSession.SessionID || seenRef.AppName != activeSession.AppName {
		t.Fatalf("AppStatusView() ref = %#v, want active session ref", seenRef)
	}
	if status.SessionID != activeSession.SessionID {
		t.Fatalf("Status().SessionID = %q, want active session", status.SessionID)
	}
	if status.Model != "test-model [high]" || status.Provider != "openai-compatible" || status.ModelName != "gpt-test" {
		t.Fatalf("Status() model fields = model=%q provider=%q name=%q, want shared app model", status.Model, status.Provider, status.ModelName)
	}
	if status.SessionMode != "manual" || status.ModeLabel != "Manual" {
		t.Fatalf("Status() mode fields = mode=%q label=%q, want shared app mode", status.SessionMode, status.ModeLabel)
	}
	if status.SandboxResolvedBackend != "host" || status.Route != "host" || !status.HostExecution {
		t.Fatalf("Status() sandbox fields = %#v, want host route from app runtime status", status)
	}
	if status.Workspace != workspace || status.Surface != "surface" {
		t.Fatalf("Status() workspace/surface = %q/%q, want %q/surface", status.Workspace, status.Surface, workspace)
	}
	if status.StoreDir != "/tmp/caelis-app-status.sqlite" {
		t.Fatalf("Status().StoreDir = %q, want shared app store URI", status.StoreDir)
	}
}

func TestGatewayDriverSubmitDoesNotRouteParticipantActiveTurnInputToActiveTurn(t *testing.T) {
	ctx := context.Background()
	activeSession := coresession.Session{
		Ref: coresession.Ref{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		Workspace: coresession.Workspace{Key: "ws", CWD: t.TempDir()},
	}
	active := []ActiveTurnState{{
		SessionRef: activeSession.Ref,
		Kind:       ActiveTurnKindParticipant,
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
	}}
	var activeSubmits []SubmitActiveTurnRequest
	var beginCalls int
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		Workspace:     activeSession.Workspace,
		ActiveTurnsFn: func() []ActiveTurnState { return active },
		StartSessionFn: func(context.Context, string, string) (coresession.Session, error) {
			return activeSession, nil
		},
		SubmitActiveTurnFn: func(_ context.Context, req SubmitActiveTurnRequest) error {
			activeSubmits = append(activeSubmits, req)
			return nil
		},
		BeginTurnFn: func(_ context.Context, req BeginTurnRequest) (BeginTurnResult, error) {
			beginCalls++
			if req.Input != "main prompt after side run" {
				t.Fatalf("BeginTurn input = %q, want trimmed main prompt", req.Input)
			}
			return BeginTurnResult{
				Session: activeSession,
				Turn:    newGatewayDriverTestTurn(activeSession.Ref),
			}, nil
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}

	_, err = driver.Submit(ctx, Submission{Text: "  main prompt after side run  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if got := len(activeSubmits); got != 0 {
		t.Fatalf("active submits = %d, want 0 for participant active turn", got)
	}
	if beginCalls != 1 {
		t.Fatalf("BeginTurn calls = %d, want 1 core main turn attempt", beginCalls)
	}
}

func TestGatewayDriverListSessionsSkipsUntitledSessions(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "resume-filter-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.StartSession(ctx, "", ""); err != nil {
		t.Fatalf("StartSession(blank) error = %v", err)
	}
	titled, err := stack.StartSessionWithTitle(ctx, "", "", "visible prompt")
	if err != nil {
		t.Fatalf("StartSession(titled) error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	candidates, err := driver.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("ListSessions() = %#v, want one titled candidate", candidates)
	}
	if candidates[0].SessionID != titled.SessionID || candidates[0].Prompt != "visible prompt" {
		t.Fatalf("ListSessions()[0] = %#v, want titled session", candidates[0])
	}
}

func TestGatewayDriverCompleteSlashArgConnectFlowUsesLegacyCommands(t *testing.T) {
	ctx := context.Background()
	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	rawCreds, err := json.Marshal(map[string]any{
		"id_token": "272182",
		"apikey":   encryptCodeFreeAPIKeyForRuntimeTest(t, "cached-api-key"),
		"baseUrl":  "https://www.srdcloud.cn",
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(credsPath, rawCreds, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	t.Setenv("CODEFREE_OAUTH_CREDS_PATH", credsPath)

	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "connect-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "connect-flow-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	providers, err := driver.CompleteSlashArg(ctx, "connect", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect) error = %v", err)
	}
	if len(providers) == 0 || providers[0].Value == "" {
		t.Fatalf("provider candidates = %#v, want non-empty", providers)
	}
	xiaomiEndpoints, err := driver.CompleteSlashArg(ctx, "connect-baseurl:xiaomi", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl:xiaomi) error = %v", err)
	}
	if !slashCandidatesHaveValue(xiaomiEndpoints, appservices.ConnectXiaomiAPIBaseURL) {
		t.Fatalf("xiaomi endpoint candidates = %#v, missing api cn", xiaomiEndpoints)
	}
	var foundTokenPlan bool
	for _, item := range xiaomiEndpoints {
		if strings.EqualFold(strings.TrimSpace(item.Value), appservices.ConnectXiaomiTokenPlanCNBaseURL) &&
			strings.Contains(item.Detail, "MIMO_TOKEN_PLAN_API_KEY") {
			foundTokenPlan = true
		}
	}
	if !foundTokenPlan {
		t.Fatalf("xiaomi endpoint candidates = %#v, missing token-plan CN OpenAI detail", xiaomiEndpoints)
	}

	models, err := driver.CompleteSlashArg(ctx, "connect-model:minimax|https%3A%2F%2Fapi.minimaxi.com%2Fanthropic|60||", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	found := false
	for _, item := range models {
		if item.Value == "MiniMax-M2.7-highspeed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("connect model candidates = %#v, want built-in MiniMax-M2.7-highspeed", models)
	}

	deepseekModels, err := driver.CompleteSlashArg(ctx, "connect-model:deepseek|https%3A%2F%2Fapi.deepseek.com%2Fv1|60||", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model deepseek) error = %v", err)
	}
	if len(deepseekModels) != 2 {
		t.Fatalf("deepseek connect model candidates = %#v, want exactly 2 built-ins", deepseekModels)
	}
	if deepseekModels[0].Value != "deepseek-v4-flash" || deepseekModels[1].Value != "deepseek-v4-pro" {
		t.Fatalf("deepseek connect model candidates = %#v, want deepseek-v4-flash and deepseek-v4-pro", deepseekModels)
	}
	for _, item := range deepseekModels {
		if !strings.Contains(item.Detail, "catalog preset") {
			t.Fatalf("deepseek connect model candidate = %#v, want catalog preset detail", item)
		}
	}

	codefreeModels, err := driver.CompleteSlashArg(ctx, "connect-model:codefree|https%3A%2F%2Fwww.srdcloud.cn|60||", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model codefree) error = %v", err)
	}
	foundCodeFree := false
	for _, item := range codefreeModels {
		if item.Value == "GLM-4.7" {
			foundCodeFree = true
			break
		}
	}
	if !foundCodeFree {
		t.Fatalf("codefree connect model candidates = %#v, want built-in GLM-4.7 without auth side effects", codefreeModels)
	}
}

func TestGatewayDriverCompleteSlashArgUsesRealModelAliases(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "slash-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	driver, err := newGatewayDriverFromTestStack(ctx, stack, "slash-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	runGatewayDriverTestCommand(t, ctx, driver, "/connect ollama alt-model")

	useCandidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(useCandidates) < 2 {
		t.Fatalf("model use candidates = %#v, want at least default and session aliases", useCandidates)
	}
	if got := useCandidates[0].Display; got != "ollama/alt-model" {
		t.Fatalf("first model use display = %q, want ollama/alt-model", got)
	}

	delCandidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	if len(delCandidates) < 2 {
		t.Fatalf("model del candidates = %#v, want at least default and session aliases", delCandidates)
	}
	if got := delCandidates[0].Display; got != "ollama/alt-model" {
		t.Fatalf("first model del display = %q, want ollama/alt-model", got)
	}
}

func TestGatewayDriverCompleteSlashArgACPModelUseOnly(t *testing.T) {
	driver := &GatewayDriver{}
	status := appviewmodel.ControllerStatus{
		ModelOptions: []appviewmodel.ControllerConfigChoice{{
			Value:       "claude-sonnet",
			Name:        "Claude Sonnet",
			Description: "remote model",
		}},
		EffortOptions: []appviewmodel.ControllerConfigChoice{{
			Value: "high",
			Name:  "High",
		}},
	}
	actions, handled := driver.completeACPControllerSlashArg(status, "model", "", 10)
	if !handled || len(actions) != 1 || actions[0].Value != "use" {
		t.Fatalf("ACP model actions = %#v handled=%v, want only use", actions, handled)
	}
	models, handled := driver.completeACPControllerSlashArg(status, "model use", "claude", 10)
	if !handled || len(models) != 1 || models[0].Value != "claude-sonnet" {
		t.Fatalf("ACP model candidates = %#v handled=%v, want remote model", models, handled)
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use claude-sonnet", "", 10)
	if !handled || len(efforts) != 1 || efforts[0].Value != "high" {
		t.Fatalf("ACP effort candidates = %#v handled=%v, want remote effort", efforts, handled)
	}
	deletes, handled := driver.completeACPControllerSlashArg(status, "model del", "", 10)
	if !handled || len(deletes) != 0 {
		t.Fatalf("ACP delete candidates = %#v handled=%v, want handled empty", deletes, handled)
	}
}

func TestGatewayDriverCompleteSlashArgACPModelUsesConfigEfforts(t *testing.T) {
	driver := &GatewayDriver{}
	status := appviewmodel.ControllerStatus{
		ModelOptions: []appviewmodel.ControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptions: []appviewmodel.ControllerConfigChoice{
			{Value: "low", Name: "Low"},
			{Value: "high", Name: "High"},
		},
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use gpt-5.5", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "high" {
		t.Fatalf("ACP gpt-5.5 efforts = %#v handled=%v, want config low/high", efforts, handled)
	}
	efforts, handled = driver.completeACPControllerSlashArg(status, "model use gpt-5.4", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "high" {
		t.Fatalf("ACP gpt-5.4 efforts = %#v handled=%v, want config low/high", efforts, handled)
	}
}

func TestGatewayDriverCompleteSlashArgACPModelUsesModelSpecificEfforts(t *testing.T) {
	driver := &GatewayDriver{}
	status := appviewmodel.ControllerStatus{
		ModelOptions: []appviewmodel.ControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptionsByModel: map[string][]appviewmodel.ControllerConfigChoice{
			"gpt-5.4": {
				{Value: "low", Name: "Low"},
				{Value: "xhigh", Name: "Xhigh"},
			},
		},
	}
	efforts, handled := driver.completeACPControllerSlashArg(status, "model use gpt-5.4", "", 10)
	if !handled || len(efforts) != 2 || efforts[0].Value != "low" || efforts[1].Value != "xhigh" {
		t.Fatalf("ACP gpt-5.4 efforts = %#v handled=%v, want model-specific low/xhigh", efforts, handled)
	}
	efforts, handled = driver.completeACPControllerSlashArg(status, "model use gpt-5.5", "", 10)
	if !handled || len(efforts) != 0 {
		t.Fatalf("ACP gpt-5.5 efforts = %#v handled=%v, want no model-specific efforts", efforts, handled)
	}
}

func TestGatewayDriverCompletesAndPersistsModelReasoningLevel(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "model-reasoning-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "deepseek",
			API:      coremodel.APIDeepSeek,
			Model:    "deepseek-v4-pro",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "model-reasoning-session", "surface", "deepseek/deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	levels, err := driver.CompleteSlashArg(ctx, "model use deepseek/deepseek-v4-pro", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use alias) error = %v", err)
	}
	if got := candidateValues(levels); !equalStrings(got, []string{"none", "high", "max"}) {
		t.Fatalf("reasoning candidates = %#v, want none/high/max", levels)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/model use deepseek/deepseek-v4-pro high")
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.Model); got != "deepseek/deepseek-v4-pro [high]" {
		t.Fatalf("status model = %q, want deepseek/deepseek-v4-pro [high]", got)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("driver has no current session")
	}
	state, err := stack.Sessions.SnapshotState(ctx, activeSession.Ref)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := strings.TrimSpace(state[appservices.StateCurrentReasoningEffort].(string)); got != "high" {
		t.Fatalf("reasoning state = %q, want high", got)
	}
}

func TestGatewayDriverConnectPersistsDeepSeekModelDefaults(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "connect-defaults-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "connect-defaults-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	status := statusAfterGatewayDriverTestCommand(t, ctx, driver, "/connect deepseek deepseek-v4-flash - secret")
	if got := status.ContextWindowTokens; got != 1048576 {
		t.Fatalf("status.ContextWindowTokens = %d, want 1048576", got)
	}
	if got := strings.TrimSpace(status.ReasoningEffort); got != "high" {
		t.Fatalf("status.ReasoningEffort = %q, want high", got)
	}

	doc, err := loadGatewayDriverTestSettings(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg appsettings.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "deepseek/deepseek-v4-flash") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want deepseek/deepseek-v4-flash", doc.Models.Configs)
	}
	if cfg.ID != "deepseek@default/deepseek/deepseek-v4-flash" {
		t.Fatalf("persisted model id = %q, want readable profile/model alias id", cfg.ID)
	}
	if cfg.ProfileID != "deepseek@default" {
		t.Fatalf("persisted profile id = %q, want deepseek@default", cfg.ProfileID)
	}
	var conn appsettings.ModelProfile
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			conn = item
			break
		}
	}
	if conn.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if conn.Provider != "deepseek" {
		t.Fatalf("persisted profile provider = %q, want deepseek", conn.Provider)
	}
	if conn.Token != "secret" || !conn.PersistToken {
		t.Fatalf("persisted profile token/persist = %q/%v, want pasted API key persisted", conn.Token, conn.PersistToken)
	}
	if conn.TokenEnv != "" {
		t.Fatalf("persisted profile token_env = %q, want empty for pasted API key", conn.TokenEnv)
	}
	if cfg.ContextWindowTokens != 1048576 {
		t.Fatalf("persisted context window = %d, want 1048576", cfg.ContextWindowTokens)
	}
	if cfg.MaxOutputTokens != 32768 {
		t.Fatalf("persisted max output = %d, want 32768", cfg.MaxOutputTokens)
	}
	if cfg.ReasoningEffort != "high" || cfg.DefaultReasoningEffort != "high" {
		t.Fatalf("persisted reasoning effort/default = %q/%q, want high/high", cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
	}
	if !equalStrings(cfg.ReasoningLevels, []string{"none", "high", "max"}) {
		t.Fatalf("persisted reasoning levels = %#v, want none/high/max", cfg.ReasoningLevels)
	}
	rawConfig, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile(config.json) error = %v", err)
	}
	raw := string(rawConfig)
	for _, forbidden := range []string{
		`"API"`,
		`"AuthType"`,
		`"HeaderKey"`,
		`"TokenEnv"`,
		`"DefaultReasoningEffort"`,
		`"ReasoningMode"`,
		`"Timeout"`,
		`"PersistToken"`,
		`"api":`,
		`"auth_type":`,
		`"header_key":`,
		`"token_env":`,
		`"default_reasoning_effort":`,
		`"timeout":`,
		`"persist_token":`,
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("config contains redundant key %s", forbidden)
		}
	}
	for _, required := range []string{
		`"profiles": [`,
		`"id": "deepseek@default"`,
		`"id": "deepseek@default/deepseek/deepseek-v4-flash"`,
		`"alias": "deepseek/deepseek-v4-flash"`,
		`"profile_id": "deepseek@default"`,
		`"provider": "deepseek"`,
		`"model": "deepseek-v4-flash"`,
		`"base_url": "https://api.deepseek.com/v1"`,
		`"token": "secret"`,
		`"context_window_tokens": 1048576`,
		`"reasoning_effort": "high"`,
		`"reasoning_mode": "toggle"`,
		`"max_output_tokens": 32768`,
	} {
		if !strings.Contains(raw, required) {
			t.Fatalf("config missing compact key %s", required)
		}
	}
}

func TestGatewayDriverConnectWithTokenEnvDoesNotPersistTokenValue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "connect-token-env-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "connect-token-env-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect deepseek deepseek-v4-flash - 60 env:DEEPSEEK_API_KEY")

	doc, err := loadGatewayDriverTestSettings(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg appsettings.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "deepseek/deepseek-v4-flash") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want deepseek/deepseek-v4-flash", doc.Models.Configs)
	}
	var conn appsettings.ModelProfile
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			conn = item
			break
		}
	}
	if conn.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if conn.Token != "" || conn.PersistToken {
		t.Fatalf("persisted profile token/persist = %q/%v, want no plaintext token for env auth", conn.Token, conn.PersistToken)
	}
	if conn.TokenEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("persisted profile token_env = %q, want DEEPSEEK_API_KEY", conn.TokenEnv)
	}
}

func TestGatewayDriverCodeFreeModelHasNoReasoningLevels(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "codefree-no-reasoning-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "codefree",
			API:      coremodel.APICodeFree,
			Model:    "GLM-5.1",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "codefree-no-reasoning-session", "surface", "codefree/glm-5.1")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	levels, err := driver.CompleteSlashArg(ctx, "model use codefree/glm-5.1", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use codefree alias) error = %v", err)
	}
	if len(levels) != 0 {
		t.Fatalf("codefree reasoning candidates = %#v, want empty", levels)
	}
}

func TestGatewayDriverConnectCodeFreeUsesExistingOAuthCache(t *testing.T) {
	ctx := context.Background()
	credsPath := filepath.Join(t.TempDir(), "oauth_creds.json")
	raw, err := json.Marshal(map[string]any{
		"id_token":            "272182",
		"apikey":              encryptCodeFreeAPIKeyForRuntimeTest(t, "cached-api-key"),
		"refresh_token":       "refresh-token",
		"baseUrl":             "https://www.srdcloud.cn",
		"expires_at_unix_ms":  time.Now().Add(time.Hour).UnixMilli(),
		"obtained_at_unix_ms": time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := os.WriteFile(credsPath, raw, 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	t.Setenv("CODEFREE_OAUTH_CREDS_PATH", credsPath)

	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "codefree-connect-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "codefree-connect-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	status := statusAfterGatewayDriverTestCommand(t, ctx, driver, "/connect codefree GLM-4.7")
	if status.Provider != "codefree" {
		t.Fatalf("provider = %q, want codefree", status.Provider)
	}
	if status.ModelName != "GLM-4.7" {
		t.Fatalf("model name = %q, want GLM-4.7", status.ModelName)
	}
}

func TestGatewayDriverStatusIncludesContextUsageSnapshot(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "status-usage-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider:            "ollama",
			API:                 coremodel.APIOllama,
			Model:               "llama3",
			ContextWindowTokens: 88000,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "status-usage-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	if _, err := stack.Sessions.AppendEvent(ctx, activeSession.Ref, coresession.Event{
		Type:    coresession.EventUser,
		Message: ptrRuntimeMessage(testCoreTextMessage(coremodel.RoleUser, "hello")),
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, activeSession.Ref, coresession.Event{
		Type:    coresession.EventAssistant,
		Message: ptrRuntimeMessage(testCoreTextMessage(coremodel.RoleAssistant, "world")),
		Meta: map[string]any{
			"provider":            "ollama",
			"model":               "llama3",
			"prompt_tokens":       12600,
			"cached_input_tokens": 9000,
			"completion_tokens":   200,
			"reasoning_tokens":    50,
			"total_tokens":        12800,
		},
	}); err != nil {
		t.Fatalf("AppendEvent(assistant) error = %v", err)
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.TotalTokens <= 12600 {
		t.Fatalf("status.TotalTokens = %d, want provider baseline plus estimated delta", status.TotalTokens)
	}
	if status.ContextWindowTokens != 88000 {
		t.Fatalf("status.ContextWindowTokens = %d, want 88000", status.ContextWindowTokens)
	}
	if status.SessionInputTokens != 12600 || status.SessionCachedInputTokens != 9000 || status.SessionOutputTokens != 200 || status.SessionReasoningTokens != 50 || status.SessionTotalTokens != 12800 {
		t.Fatalf("session token usage = input %d cached %d output %d reasoning %d total %d", status.SessionInputTokens, status.SessionCachedInputTokens, status.SessionOutputTokens, status.SessionReasoningTokens, status.SessionTotalTokens)
	}
	if status.SessionUsageMain.InputTokens != 12600 || status.SessionUsageMain.ReasoningTokens != 50 {
		t.Fatalf("main usage = %+v, want assistant usage", status.SessionUsageMain)
	}
}

func TestGatewayDriverDeleteModelRemovesConfiguredAlias(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "slash-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "delete-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect ollama alt-model")
	runGatewayDriverTestCommand(t, ctx, driver, "/model del ollama/alt-model")
	candidates, err := driver.CompleteSlashArg(ctx, "model del", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model del) error = %v", err)
	}
	for _, item := range candidates {
		if item.Value == "ollama/alt-model" {
			t.Fatalf("deleted alias still present in %#v", candidates)
		}
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Model == "ollama/alt-model" {
		t.Fatalf("status model = %q, want deleted alias removed", status.Model)
	}
}

func TestGatewayDriverDeleteOnlyModelClearsAliasCandidatesAndStatus(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "delete-only-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "delete-only-model-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect ollama llama3")
	runGatewayDriverTestCommand(t, ctx, driver, "/model del ollama/llama3")
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("model use candidates = %#v, want empty after deleting only model", candidates)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.Model) != "not configured" {
		t.Fatalf("status model = %q, want not configured after deleting only model", status.Model)
	}
}

func TestGatewayDriverUseModelResolvesCaseInsensitiveAlias(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "use-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "use-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect minimax MiniMax-M2.7-highspeed - 60 secret")
	status := statusAfterGatewayDriverTestCommand(t, ctx, driver, "/model use minimax/minimax-m2.7-highspeed")
	if got := strings.ToLower(strings.TrimSpace(status.Model)); got != "minimax/minimax-m2.7-highspeed" {
		t.Fatalf("status model = %q, want minimax/minimax-m2.7-highspeed", status.Model)
	}
}

func TestGatewayDriverAgentRegistryAndControllerUse(t *testing.T) {
	ctx := context.Background()
	repo := repoRootForGatewayDriverTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "agent-driver-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		ExternalACPAgents: []gatewayDriverTestACPAgent{{
			AgentID:     "copilot",
			AgentName:   "copilot",
			Description: "ACP sidecar agent.",
			Command:     "go",
			Args:        []string{"run", "./internal/acpe2eagent"},
			WorkDir:     repo,
			Env: []string{
				"SDK_ACP_STUB_REPLY=driver acp ok",
				"SDK_ACP_SESSION_ROOT=" + filepath.Join(root, "agent-sessions"),
				"SDK_ACP_TASK_ROOT=" + filepath.Join(root, "agent-tasks"),
			},
		}},
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "agent-driver-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	agents, err := driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if !agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents() = %#v, want assembly-registered copilot", agents)
	}
	addCandidates, err := driver.CompleteSlashArg(ctx, "agent add", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent add) error = %v", err)
	}
	for _, want := range []string{"claude", "codex", "opencode", "codefree-o", "copilot", "gemini"} {
		if !slashCandidatesHaveValue(addCandidates, want) {
			t.Fatalf("agent add candidates = %#v, want %q", addCandidates, want)
		}
	}
	if slashCandidatesHaveValue(addCandidates, "--install claude") || slashCandidatesHaveValue(addCandidates, "--install codex") {
		t.Fatalf("agent add candidates = %#v, want no install variants", addCandidates)
	}
	installCandidates, err := driver.CompleteSlashArg(ctx, "agent install", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent install) error = %v", err)
	}
	for _, want := range []string{"claude", "codex"} {
		if !slashCandidatesHaveValue(installCandidates, want) {
			t.Fatalf("agent install candidates = %#v, want %q", installCandidates, want)
		}
	}
	for _, notInstallable := range []string{"opencode", "codefree-o", "copilot", "gemini"} {
		if slashCandidatesHaveValue(installCandidates, notInstallable) {
			t.Fatalf("agent install candidates = %#v, want no %q", installCandidates, notInstallable)
		}
	}
	updateCandidates, err := driver.CompleteSlashArg(ctx, "agent update", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent update) error = %v", err)
	}
	for _, want := range []string{"claude", "codex"} {
		if !slashCandidatesHaveValue(updateCandidates, want) {
			t.Fatalf("agent update candidates = %#v, want %q", updateCandidates, want)
		}
	}
	for _, notInstallable := range []string{"opencode", "codefree-o", "copilot", "gemini"} {
		if slashCandidatesHaveValue(updateCandidates, notInstallable) {
			t.Fatalf("agent update candidates = %#v, want no %q", updateCandidates, notInstallable)
		}
	}

	_, err = driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent add copilot"})
	if err != nil {
		t.Fatalf("ExecuteCommand(/agent add copilot) error = %v", err)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus(after add) error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("agent add status = %#v, want no session participants", status)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after add) error = %v", err)
	}
	if !agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents(after add) = %#v, want attached copilot", agents)
	}
	useCandidates, err := driver.CompleteSlashArg(ctx, "agent use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent use) error = %v", err)
	}
	if !slashCandidatesHaveValue(useCandidates, "local") || !slashCandidatesHaveValue(useCandidates, "copilot") {
		t.Fatalf("agent use candidates = %#v, want local and copilot", useCandidates)
	}

	_, err = driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent use copilot"})
	if err != nil {
		t.Fatalf("ExecuteCommand(/agent use copilot) error = %v", err)
	}
	status, err = driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus(after ACP handoff) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ControllerKind)); got != "acp" {
		t.Fatalf("controller kind after ACP handoff = %q, want acp", status.ControllerKind)
	}

	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent remove copilot"}); err == nil {
		t.Fatal("ExecuteCommand(/agent remove active copilot) error = nil, want use local first")
	}
	_, err = driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent use local"})
	if err != nil {
		t.Fatalf("ExecuteCommand(/agent use local) error = %v", err)
	}
	status, err = driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus(after local handoff) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ControllerKind)); got != "kernel" {
		t.Fatalf("controller kind after local handoff = %q, want kernel", status.ControllerKind)
	}

	removeCandidates, err := driver.CompleteSlashArg(ctx, "agent remove", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent remove) error = %v", err)
	}
	if len(removeCandidates) != 1 || removeCandidates[0].Value != "copilot" {
		t.Fatalf("agent remove candidates = %#v, want registered copilot", removeCandidates)
	}

	_, err = driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent remove copilot"})
	if err != nil {
		t.Fatalf("ExecuteCommand(/agent remove copilot) error = %v", err)
	}
	status, err = driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus(after remove) error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("agent remove status = %#v, want zero participants", status)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after remove) error = %v", err)
	}
	if agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents(after remove) = %#v, want copilot removed", agents)
	}
}

func TestGatewayDriverDynamicAgentCommandDoesNotPersistParticipantOnPromptConflict(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	engine := &appServiceDriverEngine{}
	svc, err := appservices.New(appservices.Config{
		Runtime: coreconfig.Runtime{
			AppName:      "caelis",
			UserID:       "agent-conflict-rollback-test",
			WorkspaceKey: "ws",
			WorkspaceCWD: root,
		},
		Engine: engine,
		Agents: []appservices.AgentDescriptor{{
			ID:          "copilot",
			Name:        "copilot",
			Kind:        appservices.AgentKindExternalACP,
			Description: "ACP sidecar agent.",
			Command:     "copilot-acp",
		}},
		Invokers: map[string]appservices.AgentInvoker{
			"copilot": appservices.AgentInvokerFunc(func(context.Context, appservices.AgentInvokeRequest) (appservices.AgentInvokeResult, error) {
				return appservices.AgentInvokeResult{}, errGatewayDriverTestActiveRunConflict
			}),
		},
	})
	if err != nil {
		t.Fatalf("appservices.New() error = %v", err)
	}
	driver, err := NewGatewayDriver(ctx, BindAppServices(&DriverStack{}, svc), "agent-conflict-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}
	imageRaw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "side.png"), imageRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = driver.ExecuteCommand(ctx, CommandExecutionOptions{
		Input:       "/copilot second prompt",
		Attachments: []Attachment{{Name: "side.png", Offset: len([]rune("second "))}},
	})
	if err == nil {
		t.Fatal("ExecuteCommand(/copilot) error = nil, want active run conflict")
	}
	if !errors.Is(err, errGatewayDriverTestActiveRunConflict) {
		t.Fatalf("ExecuteCommand(/copilot) error = %v, want active run conflict", err)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("AgentStatus().Participants = %#v, want no partial sidecar after failed command", status.Participants)
	}
	if len(engine.events) != 0 {
		t.Fatalf("recorded events = %#v, want no partial sidecar events after failed command", engine.events)
	}
}

func TestGatewayDriverStatusUsesPersistedDefaultAliasOnStartup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "status-startup-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	prepared, err := stack.services.Models().PrepareConnectConfig(ctx, appsettings.ModelConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		Token:    "secret",
	})
	if err != nil {
		t.Fatalf("PrepareConnectConfig() error = %v", err)
	}
	if _, err := stack.services.Models().Connect(ctx, prepared); err != nil {
		t.Fatalf("Models().Connect() error = %v", err)
	}

	reloaded, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "status-startup-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, reloaded, "startup-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.Model); got != "deepseek/deepseek-v4-pro [high]" {
		t.Fatalf("status model = %q, want deepseek/deepseek-v4-pro [high]", status.Model)
	}
}

func TestGatewayDriverStartupUsesRequestedSessionID(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "lazy-session-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected startup driver to create an active session")
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.SessionID) == "" {
		t.Fatal("expected startup status to include active session id")
	}
	if status.SessionID != activeSession.SessionID {
		t.Fatalf("status session = %q, want %q", status.SessionID, activeSession.SessionID)
	}
	if status.SessionID != "sticky-session" {
		t.Fatalf("session id = %q, want sticky-session from constructor hint", status.SessionID)
	}
}

func TestGatewayDriverStartupBindsRequestedSessionInsteadOfFreshOne(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "binding-reset-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	stale, err := stack.StartSession(ctx, "stale-session", "surface")
	if err != nil {
		t.Fatalf("StartSession(stale) error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if strings.TrimSpace(status.SessionID) == "" {
		t.Fatal("expected startup driver to bind the requested session")
	}
	if status.SessionID != "sticky-session" {
		t.Fatalf("startup session = %q, want sticky-session", status.SessionID)
	}
	if status.SessionID == stale.SessionID {
		t.Fatalf("startup session = %q, want sticky-session instead of stale bound session", status.SessionID)
	}
	current, ok := stack.CurrentSession("surface")
	if !ok {
		t.Fatal("expected surface binding to exist after startup")
	}
	if current.SessionID != status.SessionID {
		t.Fatalf("current binding session = %q, want %q", current.SessionID, status.SessionID)
	}
}

func TestGatewayDriverStartupReusesExistingRequestedSession(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "startup-resume-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	existing, err := stack.StartSession(ctx, "sticky-session", "other-surface")
	if err != nil {
		t.Fatalf("StartSession(sticky-session) error = %v", err)
	}

	driver, err := newGatewayDriverFromTestStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionID != existing.SessionID {
		t.Fatalf("status session = %q, want existing session %q", status.SessionID, existing.SessionID)
	}
}

func TestGatewayDriverCycleSessionModeUsesStartupSession(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "lazy-session-mode-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	startup, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected startup session")
	}
	status := statusAfterGatewayDriverTestCommand(t, ctx, driver, "/approval toggle")
	if strings.TrimSpace(status.SessionID) == "" {
		t.Fatal("expected CycleSessionMode() to keep an active session")
	}
	if status.SessionID != startup.SessionID {
		t.Fatalf("session id = %q, want startup session %q", status.SessionID, startup.SessionID)
	}
	if status.SessionMode != "manual" {
		t.Fatalf("session mode = %q, want manual", status.SessionMode)
	}
}

func TestNextACPControllerModeUsesDeclaredModeOrder(t *testing.T) {
	status := appviewmodel.ControllerStatus{
		Mode: "default",
		ModeOptions: []appviewmodel.ControllerMode{
			{ID: "default", Name: "Default"},
			{ID: "review", Name: "Review"},
			{ID: "plan", Name: "Plan"},
		},
	}
	next, err := nextACPControllerMode(status)
	if err != nil {
		t.Fatalf("nextACPControllerMode() error = %v", err)
	}
	if next.ID != "review" {
		t.Fatalf("next mode = %#v, want review", next)
	}

	status.Mode = "Plan"
	next, err = nextACPControllerMode(status)
	if err != nil {
		t.Fatalf("nextACPControllerMode(name) error = %v", err)
	}
	if next.ID != "default" {
		t.Fatalf("next mode from name = %#v, want default", next)
	}
}

func TestACPControllerModeDisplayPrefersDeclaredName(t *testing.T) {
	status := appviewmodel.ControllerStatus{
		Mode: "review",
		ModeOptions: []appviewmodel.ControllerMode{
			{ID: "review", Name: "Review"},
		},
	}
	if got := acpControllerModeDisplay(status); got != "Review" {
		t.Fatalf("acpControllerModeDisplay() = %q, want Review", got)
	}
	status.ModeOptions = nil
	if got := acpControllerModeDisplay(status); got != "review" {
		t.Fatalf("acpControllerModeDisplay() fallback = %q, want review", got)
	}
}

func TestGatewayDriverACPStatusPrefersRemoteModeOverLocalSessionMode(t *testing.T) {
	ctx := context.Background()
	ref := coresession.Ref{AppName: "caelis", UserID: "u", SessionID: "parent", WorkspaceKey: "ws"}
	activeSession := coresession.Session{
		Ref:       ref,
		Workspace: coresession.Workspace{Key: "ws", CWD: t.TempDir()},
		Controller: coresession.ControllerBinding{
			Kind:            coresession.ControllerACP,
			AgentName:       "opencode",
			RemoteSessionID: "remote-1",
		},
	}
	driver := &GatewayDriver{
		stack: &DriverStack{
			Workspace: activeSession.Workspace,
			AppStatusViewFn: func(_ context.Context, ref coresession.Ref, _ bool) (appviewmodel.StatusView, error) {
				return testGatewayStatusView(ref, activeSession.Workspace, "local/model", "local-default"), nil
			},
			ACPControllerStatusFn: func(context.Context, coresession.Ref) (appviewmodel.ControllerStatus, bool, error) {
				return appviewmodel.ControllerStatus{
					Model: "remote-model",
					Mode:  "code",
					ModeOptions: []appviewmodel.ControllerMode{
						{ID: "code", Name: "Code"},
					},
				}, true, nil
			},
		},
		session:            activeSession,
		hasSession:         true,
		defaultSessionMode: "local-default",
		sessionMode:        "local-default",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}

	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionMode != "code" || status.ModeLabel != "Code" {
		t.Fatalf("status mode/label = %q/%q, want remote code/Code", status.SessionMode, status.ModeLabel)
	}
	if status.Provider != "acp" || status.Model != "remote-model" {
		t.Fatalf("status provider/model = %q/%q, want acp/remote-model", status.Provider, status.Model)
	}
}

func TestGatewayDriverACPStatusKeepsAgentFallbackWithoutRemoteModel(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "acp-model-fallback-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "minimax",
			Model:    "MiniMax-M2",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "acp-fallback-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = stack.Sessions.BindController(ctx, activeSession.Ref, coresession.ControllerBinding{
		Kind:            coresession.ControllerACP,
		ID:              "codex",
		Label:           "Codex ACP",
		RemoteSessionID: "remote-1",
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	driver := &GatewayDriver{
		stack:              gatewayDriverTestRuntimeStack(stack),
		session:            activeSession,
		hasSession:         true,
		bindingKey:         "surface",
		defaultSessionMode: "default",
		sessionMode:        "default",
		defaultSandboxType: "host",
		sandboxType:        "host",
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Provider != "acp" {
		t.Fatalf("provider = %q, want acp", status.Provider)
	}
	if status.Model != "Codex ACP" {
		t.Fatalf("model = %q, want ACP agent fallback instead of local model", status.Model)
	}
}

func TestGatewayDriverIgnoresStaleSessionAliasOutsideConfiguredModels(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "stale-session-alias-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "stale-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	activeSession, err := driver.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, activeSession.Ref, func(state map[string]any) (map[string]any, error) {
		next := cloneTestState(state)
		if next == nil {
			next = map[string]any{}
		}
		next["kernel.current_model_alias"] = "minimax/minimax-m2.7-highspeed"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.Model); got != "not configured" {
		t.Fatalf("status model = %q, want not configured because alias is stale", status.Model)
	}
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	for _, item := range candidates {
		if strings.EqualFold(strings.TrimSpace(item.Value), "minimax/minimax-m2.7-highspeed") {
			t.Fatalf("stale session alias leaked into candidates: %#v", candidates)
		}
	}
}

func TestGatewayDriverCompleteSlashArgUsesPrefixMatching(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "prefix-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "prefix-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	modelActions, err := driver.CompleteSlashArg(ctx, "model", "de", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model, de) error = %v", err)
	}
	if len(modelActions) != 1 || modelActions[0].Value != "del" {
		t.Fatalf("model action candidates = %#v, want only del", modelActions)
	}

	runGatewayDriverTestCommand(t, ctx, driver, "/connect deepseek deepseek-v4-pro - 60 env:DEEPSEEK_API_KEY")
	modelAliases, err := driver.CompleteSlashArg(ctx, "model use", "dee", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use, dee) error = %v", err)
	}
	if len(modelAliases) == 0 || modelAliases[0].Display != "deepseek/deepseek-v4-pro" {
		t.Fatalf("model alias candidates = %#v, want deepseek/deepseek-v4-pro first", modelAliases)
	}
	deepseekLevels, err := driver.CompleteSlashArg(ctx, "model use deepseek/deepseek-v4-pro", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use deepseek alias) error = %v", err)
	}
	if got := candidateValues(deepseekLevels); !equalStrings(got, []string{"none", "high", "max"}) {
		t.Fatalf("deepseek reasoning candidates = %#v, want none/high/max", deepseekLevels)
	}
}

func TestGatewayDriverCompleteSlashArgSettingsUsesSharedPanel(t *testing.T) {
	ctx := context.Background()
	panel := appviewmodel.SettingsPanelView{
		Configured: true,
		Sections: []appviewmodel.SettingsPanelSection{{
			ID:    "runtime",
			Title: "Runtime",
			Fields: []appviewmodel.SettingsPanelField{{
				ID:       "runtime.app_name",
				Label:    "App",
				Kind:     "text",
				Value:    "caelis",
				Editable: false,
			}},
		}, {
			ID:    "sandbox",
			Title: "Sandbox",
			Fields: []appviewmodel.SettingsPanelField{{
				ID:          "sandbox.backend",
				ConfigID:    "sandbox_backend",
				Label:       "Requested backend",
				Kind:        "select",
				Value:       "host",
				Editable:    true,
				Description: "Choose the requested sandbox backend",
				Options: []appviewmodel.SettingsPanelFieldOption{
					{Value: "host", Label: "Host"},
					{Value: "seatbelt", Label: "Seatbelt"},
				},
			}},
			Actions: []appviewmodel.SettingsPanelAction{{
				ID:          "sandbox.prepare",
				Label:       "Prepare",
				Description: "Prepare sandbox",
				Enabled:     true,
			}, {
				ID:                   "sandbox.reset",
				Label:                "Reset",
				Description:          "Reset sandbox",
				Enabled:              true,
				Destructive:          true,
				RequiresConfirmation: true,
			}},
		}},
		Actions: []appviewmodel.SettingsPanelAction{{
			ID:          "sandbox.prepare",
			Label:       "Prepare",
			Description: "Prepare sandbox",
			Enabled:     true,
		}, {
			ID:                   "sandbox.reset",
			Label:                "Reset",
			Description:          "Reset sandbox",
			Enabled:              true,
			Destructive:          true,
			RequiresConfirmation: true,
		}},
	}
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		SettingsPanelFn: func(context.Context, coresession.Ref) (appviewmodel.SettingsPanelView, error) {
			return panel, nil
		},
	}, "", "surface", "")
	if err != nil {
		t.Fatal(err)
	}

	root, err := driver.CompleteSlashArg(ctx, "settings", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(settings) error = %v", err)
	}
	if got := candidateValues(root); !equalStrings(got, []string{"set", "run"}) {
		t.Fatalf("settings root candidates = %#v, want set/run", root)
	}
	fields, err := driver.CompleteSlashArg(ctx, "settings set", "sand", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(settings set) error = %v", err)
	}
	if !slashCandidatesHaveValue(fields, "sandbox.backend") || slashCandidatesHaveValue(fields, "runtime.app_name") {
		t.Fatalf("settings field candidates = %#v, want editable sandbox field only", fields)
	}
	values, err := driver.CompleteSlashArg(ctx, "settings set sandbox.backend", "sea", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(settings set sandbox.backend) error = %v", err)
	}
	if got := candidateValues(values); !equalStrings(got, []string{"seatbelt"}) {
		t.Fatalf("settings value candidates = %#v, want seatbelt", values)
	}
	actions, err := driver.CompleteSlashArg(ctx, "settings run", "reset", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(settings run) error = %v", err)
	}
	if got := candidateValues(actions); !equalStrings(got, []string{"sandbox.reset"}) {
		t.Fatalf("settings action candidates = %#v, want sandbox.reset", actions)
	}
	confirm, err := driver.CompleteSlashArg(ctx, "settings run sandbox.reset", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(settings run sandbox.reset) error = %v", err)
	}
	if got := candidateValues(confirm); !equalStrings(got, []string{"confirm"}) {
		t.Fatalf("settings confirm candidates = %#v, want confirm", confirm)
	}
}

func TestGatewayDriverCompleteSlashArgAgentRootOrder(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "agent-root-order-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "agent-root-order-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "agent", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent) error = %v", err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.Value)
	}
	want := []string{"use", "add", "install", "update", "list", "remove"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agent root candidates = %#v, want %#v", got, want)
	}
}

func TestGatewayDriverInterruptCancelsAgentInstall(t *testing.T) {
	ctx := context.Background()
	binDir := t.TempDir()
	started := filepath.Join(t.TempDir(), "npm-started")
	npmPath := filepath.Join(binDir, testenv.CommandScriptName("npm"))
	body := "#!/bin/sh\nprintf started > \"$CAELIS_NPM_STARTED\"\nwhile true; do /bin/sleep 1; done\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\necho started> \"%CAELIS_NPM_STARTED%\"\r\n:loop\r\nping -n 2 127.0.0.1 >nul\r\ngoto loop\r\n"
	}
	if err := os.WriteFile(npmPath, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(npm) error = %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("CAELIS_NPM_STARTED", started)
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "agent-install-cancel-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "agent-install-cancel-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/agent install claude"})
		done <- err
	}()

	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("ExecuteCommand(/agent install claude) returned before fake npm started: %v", err)
		case <-deadline:
			t.Fatal("fake npm did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := driver.Interrupt(ctx); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteCommand(/agent install claude) error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteCommand(/agent install claude) did not return after Interrupt")
	}
}

func TestGatewayDriverConnectPersistsMultipleProviders(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "multi-provider-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "multi-provider-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect minimax MiniMax-M2.7-highspeed - 60 minimax-secret")
	runGatewayDriverTestCommand(t, ctx, driver, "/connect deepseek deepseek-v4-pro - 60 deepseek-secret")
	candidates, err := driver.CompleteSlashArg(ctx, "model use", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("model use candidates = %#v, want both providers", candidates)
	}
	if candidates[0].Display != "deepseek/deepseek-v4-pro" {
		t.Fatalf("first candidate display = %q, want deepseek/deepseek-v4-pro", candidates[0].Display)
	}
	foundMinimax := false
	for _, candidate := range candidates {
		if candidate.Display == "minimax/minimax-m2.7-highspeed" {
			foundMinimax = true
			break
		}
	}
	if !foundMinimax {
		t.Fatalf("model use candidates = %#v, missing minimax alias", candidates)
	}
}

func TestGatewayDriverConnectXiaomiTokenPlanCNStoresXiaomiProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "xiaomi-token-plan-connect-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "xiaomi-token-plan-connect-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect xiaomi mimo-v2.5-pro "+appservices.ConnectXiaomiTokenPlanCNBaseURL+" 60 env:MIMO_TOKEN_PLAN_API_KEY")

	doc, err := loadGatewayDriverTestSettings(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg appsettings.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "xiaomi/mimo-v2.5-pro") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want xiaomi alias", doc.Models.Configs)
	}
	if cfg.ID != "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("persisted model id = %q, want readable profile/model alias id", cfg.ID)
	}
	if cfg.ProfileID != "xiaomi@token-plan-cn" {
		t.Fatalf("persisted profile id = %q, want xiaomi@token-plan-cn", cfg.ProfileID)
	}
	var profile appsettings.ModelProfile
	for _, item := range doc.Models.Profiles {
		if strings.EqualFold(item.ID, cfg.ProfileID) {
			profile = item
			break
		}
	}
	if profile.ID == "" {
		t.Fatalf("persisted profiles = %#v, missing %q", doc.Models.Profiles, cfg.ProfileID)
	}
	if profile.Provider != "xiaomi" {
		t.Fatalf("profile provider = %q, want xiaomi", profile.Provider)
	}
	if profile.BaseURL != appservices.ConnectXiaomiTokenPlanCNBaseURL {
		t.Fatalf("profile base_url = %q, want %q", profile.BaseURL, appservices.ConnectXiaomiTokenPlanCNBaseURL)
	}
	if profile.TokenEnv != "MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("profile token_env = %q, want MIMO_TOKEN_PLAN_API_KEY", profile.TokenEnv)
	}
}

func TestGatewayDriverConnectXiaomiEndpointsCoexistUnderVisibleAlias(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "xiaomi-endpoint-coexist-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "xiaomi-endpoint-coexist-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	for _, input := range []string{
		"/connect xiaomi mimo-v2.5-pro " + appservices.ConnectXiaomiAPIBaseURL + " 60 env:XIAOMI_API_KEY",
		"/connect xiaomi mimo-v2.5-pro " + appservices.ConnectXiaomiTokenPlanCNBaseURL + " 60 env:MIMO_TOKEN_PLAN_API_KEY",
	} {
		runGatewayDriverTestCommand(t, ctx, driver, input)
	}

	doc, err := loadGatewayDriverTestSettings(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var sameAlias int
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "xiaomi/mimo-v2.5-pro") {
			sameAlias++
		}
	}
	if sameAlias != 2 {
		t.Fatalf("persisted configs = %#v, want two xiaomi/mimo-v2.5-pro bindings", doc.Models.Configs)
	}
	if len(doc.Models.Profiles) != 2 {
		t.Fatalf("persisted profiles = %#v, want two endpoint profiles", doc.Models.Profiles)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "model use", "xiaomi/mimo-v2.5-pro", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use) error = %v", err)
	}
	var apiCandidate, tokenPlanCandidate SlashArgCandidate
	for _, candidate := range candidates {
		if candidate.Display != "xiaomi/mimo-v2.5-pro" {
			continue
		}
		switch {
		case strings.Contains(candidate.Detail, "api-cn"):
			apiCandidate = candidate
		case strings.Contains(candidate.Detail, "token-plan-cn"):
			tokenPlanCandidate = candidate
		}
	}
	if apiCandidate.Value == "" || tokenPlanCandidate.Value == "" || apiCandidate.Value == tokenPlanCandidate.Value {
		t.Fatalf("model use candidates = %#v, want distinct hidden ids for both endpoints", candidates)
	}
	if apiCandidate.Value != "xiaomi@api-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("api candidate value = %q, want readable api profile/model id", apiCandidate.Value)
	}
	if tokenPlanCandidate.Value != "xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro" {
		t.Fatalf("token-plan candidate value = %q, want readable token-plan profile/model id", tokenPlanCandidate.Value)
	}
	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/model use xiaomi/mimo-v2.5-pro"}); err == nil || !strings.Contains(err.Error(), "ambiguous model alias") {
		t.Fatalf("UseModel(visible alias) error = %v, want ambiguity", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/model use "+tokenPlanCandidate.Value)
}

func TestGatewayDriverConnectReusesExistingEndpointAuth(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "connect-reuse-auth-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "connect-reuse-auth-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect xiaomi mimo-v2.5-pro "+appservices.ConnectXiaomiAPIBaseURL+" 60 env:XIAOMI_API_KEY")
	endpoints, err := driver.CompleteSlashArg(ctx, "connect-baseurl:xiaomi", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl:xiaomi) error = %v", err)
	}
	var foundReusable bool
	for _, endpoint := range endpoints {
		if endpoint.Value == appservices.ConnectXiaomiAPIBaseURL && endpoint.NoAuth && strings.Contains(endpoint.Detail, "configured auth") {
			foundReusable = true
			break
		}
	}
	if !foundReusable {
		t.Fatalf("endpoint candidates = %#v, want reusable auth marker for api cn", endpoints)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect xiaomi mimo-v2-pro "+appservices.ConnectXiaomiAPIBaseURL+" 60 -")
	doc, err := loadGatewayDriverTestSettings(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	if len(doc.Models.Profiles) != 1 {
		t.Fatalf("persisted profiles = %#v, want one shared profile", doc.Models.Profiles)
	}
	if got := doc.Models.Profiles[0].TokenEnv; got != "XIAOMI_API_KEY" {
		t.Fatalf("shared profile token_env = %q, want XIAOMI_API_KEY", got)
	}
}

func TestGatewayDriverCompleteFileUsesRelativePathsAndSkipsNoise(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "src", "pkg"), 0o700); err != nil {
		t.Fatalf("MkdirAll(src/pkg) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "node_modules", "leftpad"), 0o700); err != nil {
		t.Fatalf("MkdirAll(node_modules) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "objects"), 0o700); err != nil {
		t.Fatalf("MkdirAll(.git) error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(workspace, "src", "main.go"),
		filepath.Join(workspace, "src", "pkg", "helper.go"),
		filepath.Join(workspace, "node_modules", "leftpad", "index.js"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}

	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "file-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "file-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	candidates, err := driver.CompleteFile(ctx, "src/ma", 10)
	if err != nil {
		t.Fatalf("CompleteFile() error = %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("CompleteFile() returned no candidates, want src/main.go")
	}
	if got := candidates[0].Value; got != "src/main.go" {
		t.Fatalf("first candidate value = %q, want src/main.go", got)
	}

	all, err := driver.CompleteFile(ctx, "", 20)
	if err != nil {
		t.Fatalf("CompleteFile(all) error = %v", err)
	}
	for _, item := range all {
		if strings.Contains(item.Value, "node_modules") || strings.Contains(item.Value, ".git") {
			t.Fatalf("noise directory leaked into candidates: %#v", all)
		}
	}
}

func TestGatewayDriverCompleteSkillDiscoversGlobalAndWorkspaceSkills(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	setHomeForGatewayDriverTest(t, home)

	globalSkill := filepath.Join(home, ".agents", "skills", "echo")
	workspaceSkill := filepath.Join(workspace, ".agents", "skills", "lint")
	for _, dir := range []string{globalSkill, workspaceSkill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(globalSkill, "SKILL.md"), []byte("---\nname: echo\ndescription: Echo text.\n---\n# Echo\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(global SKILL.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceSkill, "SKILL.md"), []byte("---\nname: lint\ndescription: Run lint checks.\n---\n# Lint\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(workspace SKILL.md) error = %v", err)
	}

	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "skill-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "skill-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}

	candidates, err := driver.CompleteSkill(ctx, "", 10)
	if err != nil {
		t.Fatalf("CompleteSkill() error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("CompleteSkill() = %#v, want global and workspace skills", candidates)
	}
	foundEcho := false
	foundLint := false
	for _, item := range candidates {
		switch item.Value {
		case "echo":
			foundEcho = strings.Contains(item.Detail, "Echo text") && strings.TrimSpace(item.Path) != ""
		case "lint":
			foundLint = strings.Contains(item.Detail, "Run lint checks") && strings.TrimSpace(item.Path) != ""
		}
	}
	if !foundEcho || !foundLint {
		t.Fatalf("CompleteSkill() = %#v, want echo and lint metadata", candidates)
	}
}

func TestGatewayDriverCompleteMentionReturnsACPSidecarsOnly(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "mention-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "mention-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	activeSession, err := driver.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession() error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, activeSession.Ref, coresession.ParticipantBinding{
		ID:           "side-1",
		Kind:         coresession.ParticipantACP,
		Role:         coresession.ParticipantSidecar,
		AgentName:    "codex",
		Label:        "@jeff",
		SessionID:    "child-1",
		Source:       "custom_codex",
		DelegationID: "task-side",
	}); err != nil {
		t.Fatalf("PutParticipant(side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, activeSession.Ref, coresession.ParticipantBinding{
		ID:           "restored-side-1",
		Kind:         coresession.ParticipantSubagent,
		Role:         coresession.ParticipantSidecar,
		AgentName:    "restored",
		Label:        "@jill",
		SessionID:    "restored-child-1",
		DelegationID: "task-restored",
	}); err != nil {
		t.Fatalf("PutParticipant(restored-side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, activeSession.Ref, coresession.ParticipantBinding{
		ID:           "task-1",
		Kind:         coresession.ParticipantSubagent,
		Role:         coresession.ParticipantDelegated,
		Label:        "@jude",
		SessionID:    "child-2",
		DelegationID: "task-1",
	}); err != nil {
		t.Fatalf("PutParticipant(delegated) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, activeSession.Ref, coresession.ParticipantBinding{
		ID:           "self-001",
		Kind:         coresession.ParticipantSubagent,
		Role:         coresession.ParticipantDelegated,
		AgentName:    "self",
		Label:        "@jude",
		SessionID:    "self-child-1",
		DelegationID: "task-self",
	}); err != nil {
		t.Fatalf("PutParticipant(self) error = %v", err)
	}
	candidates, err := driver.CompleteMention(ctx, "j", 8)
	if err != nil {
		t.Fatalf("CompleteMention() error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].Value != "jeff" || candidates[0].Display != "jeff(codex)" {
		t.Fatalf("CompleteMention() = %#v, want side target", candidates)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 2 || status.Participants[0].ID != "side-1" || status.Participants[1].ID != "restored-side-1" {
		t.Fatalf("AgentStatus().Participants = %#v, want visible side participants", status.Participants)
	}
	if len(status.DelegatedParticipants) != 2 || status.DelegatedParticipants[0].ID != "task-1" || status.DelegatedParticipants[1].ID != "self-001" {
		t.Fatalf("AgentStatus().DelegatedParticipants = %#v, want delegated task summary", status.DelegatedParticipants)
	}
}

func TestGatewayDriverCompleteResumeIncludesMetadataAndRecentFirst(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "resume-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	first, err := stack.StartSessionWithTitle(ctx, "", "first-binding", "First Task")
	if err != nil {
		t.Fatalf("StartSession(first) error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, first.Ref, func(state map[string]any) (map[string]any, error) {
		next := cloneTestState(state)
		next[appservices.StateCurrentModelID] = "openai/gpt-4o-mini"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState(first) error = %v", err)
	}
	second, err := stack.StartSessionWithTitle(ctx, "", "second-binding", "Second Task")
	if err != nil {
		t.Fatalf("StartSession(second) error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, second.Ref, func(state map[string]any) (map[string]any, error) {
		next := cloneTestState(state)
		next[appservices.StateCurrentModelID] = "deepseek/deepseek-v4-flash"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState(second) error = %v", err)
	}

	driver, err := newGatewayDriverFromTestStack(ctx, stack, "resume-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	candidates, err := driver.CompleteResume(ctx, "task", 10)
	if err != nil {
		t.Fatalf("CompleteResume() error = %v", err)
	}
	if len(candidates) < 2 {
		t.Fatalf("CompleteResume() = %#v, want at least two sessions", candidates)
	}
	if candidates[0].Title != "Second Task" {
		t.Fatalf("first resume candidate title = %q, want most recent Second Task", candidates[0].Title)
	}
	if candidates[0].Model == "" || candidates[0].Workspace == "" {
		t.Fatalf("first resume candidate = %#v, want model and workspace metadata", candidates[0])
	}
}

func TestGatewayDriverDeleteModelRejectsUnknownAlias(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "delete-unknown-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "delete-unknown-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/model del minimax/minimax-m1"}); err == nil {
		t.Fatal("DeleteModel() error = nil, want unknown alias error")
	}
}

func TestGatewayDriverConnectModelCandidatesIncludeConfiguredProviderModels(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "connect-candidates-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Model: ModelConfig{
			Provider: "ollama",
			API:      coremodel.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "connect-candidates-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect minimax MiniMax-M2.7-highspeed - 60 secret")

	models, err := driver.CompleteSlashArg(ctx, "connect-model:minimax|https%3A%2F%2Fapi.minimaxi.com%2Fanthropic|60|secret|", "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model) error = %v", err)
	}
	found := false
	for _, item := range models {
		if item.Value == "MiniMax-M2.7-highspeed" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("connect model candidates = %#v, want configured minimax model", models)
	}
}

func TestGatewayDriverConnectRejectsMissingAPIKeyWithActionableError(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "missing-key-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "missing-key-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/connect openai gpt-4o"}); err == nil || !strings.Contains(err.Error(), "env:OPENAI_API_KEY") {
		t.Fatalf("Connect() error = %v, want actionable env hint", err)
	}
}

func TestGatewayDriverConnectRejectsInvalidBaseURL(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "invalid-baseurl-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "invalid-baseurl-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	if _, err := driver.ExecuteCommand(ctx, CommandExecutionOptions{Input: "/connect openai-compatible gpt-4o not-a-url 60 secret"}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "base url is invalid") {
		t.Fatalf("Connect() error = %v, want invalid base URL guidance", err)
	}
}

func TestGatewayDriverStatusIncludesDoctorDiagnostics(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayDriverTestConfig{
		AppName:        "caelis",
		UserID:         "doctor-status-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromTestStack(ctx, stack, "doctor-status-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromTestStack() error = %v", err)
	}
	runGatewayDriverTestCommand(t, ctx, driver, "/connect minimax MiniMax-M2.7-highspeed - 60 env:MINIMAX_API_KEY")
	runGatewayDriverTestCommand(t, ctx, driver, "/approval manual")
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.StoreDir != root {
		t.Fatalf("status.StoreDir = %q, want %q", status.StoreDir, root)
	}
	if status.Provider != "minimax" || status.ModelName != "MiniMax-M2.7-highspeed" {
		t.Fatalf("status provider/model = %q/%q, want minimax/MiniMax-M2.7-highspeed", status.Provider, status.ModelName)
	}
	if !status.MissingAPIKey {
		t.Fatal("status.MissingAPIKey = false, want true when token env is unset")
	}
	if !status.HostExecution || status.FullAccessMode {
		t.Fatalf("status host/full_access = %v/%v, want true/false", status.HostExecution, status.FullAccessMode)
	}
}

func TestGatewayDriverStatusIncludesPermissionGrantSummary(t *testing.T) {
	ctx := context.Background()
	activeSession := coresession.Session{
		Ref:       coresession.Ref{SessionID: "grant-session"},
		Workspace: coresession.Workspace{CWD: "/workspace"},
	}
	driver := &GatewayDriver{
		stack: &DriverStack{
			Workspace: coresession.Workspace{CWD: "/workspace"},
			AppStatusViewFn: func(_ context.Context, ref coresession.Ref, _ bool) (appviewmodel.StatusView, error) {
				view := testGatewayStatusView(ref, activeSession.Workspace, "grant-model", "auto-review")
				view.Permissions = appviewmodel.PermissionStatus{
					GrantCount:     2,
					ReadRootCount:  3,
					WriteRootCount: 1,
				}
				return view, nil
			},
		},
		session:    activeSession,
		hasSession: true,
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.PermissionGrantCount != 2 || status.PermissionReadRootCount != 3 || status.PermissionWriteRootCount != 1 {
		t.Fatalf("permission grant summary = count:%d read:%d write:%d, want 2/3/1", status.PermissionGrantCount, status.PermissionReadRootCount, status.PermissionWriteRootCount)
	}
}

func repoRootForGatewayDriverTest(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

func setHomeForGatewayDriverTest(t *testing.T, home string) {
	t.Helper()
	testenv.SetHome(t, home)
}

func agentCandidatesHaveName(candidates []AgentCandidate, name string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func slashCandidatesHaveValue(candidates []SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func candidateValues(candidates []SlashArgCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, strings.TrimSpace(candidate.Value))
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
