package gatewaydriver

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/impl/model/providers"
	"github.com/OnslaughtSnail/caelis/internal/agenthandle"
	"github.com/OnslaughtSnail/caelis/internal/testenv"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
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

func ptrRuntimeMessage(message model.Message) *model.Message {
	return &message
}

func modelUsageMetaForRuntimeTest(prompt int, cached int, completion int, total int, reasoning ...int) map[string]any {
	reasoningTokens := 0
	if len(reasoning) > 0 {
		reasoningTokens = reasoning[0]
	}
	return map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"sdk": map[string]any{
				"usage": map[string]any{
					"prompt_tokens":       prompt,
					"cached_input_tokens": cached,
					"completion_tokens":   completion,
					"reasoning_tokens":    reasoningTokens,
					"total_tokens":        total,
				},
			},
		},
	}
}

func closeGatewayDriverTestTurn(t *testing.T, turn Turn) {
	t.Helper()
	if turn == nil {
		return
	}
	turn.Cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-turn.Events():
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

func newGatewayDriverTestStack(t *testing.T, cfg gatewayapp.Config) (*gatewayapp.Stack, error) {
	t.Helper()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	return gatewayapp.NewLocalStack(cfg)
}

func TestGatewayDriverUsesCurrentGatewayAfterSandboxRebuild(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "driver-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   "driver-workspace",
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Sandbox: gatewayapp.SandboxConfig{
			HelperPath: filepath.Join(t.TempDir(), "missing-landlock-helper"),
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "rebuild-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	before := stack.CurrentGateway()
	if got, err := driver.gateway(); err != nil || got != before {
		t.Fatalf("driver.gateway() before rebuild = %p, %v; want %p", got, err, before)
	}
	// This test only needs to force a gateway rebuild; the missing helper keeps
	// auto landlock fallback from recursively executing this test binary in CI.
	if _, err := stack.SetSandboxBackend(ctx, "auto"); err != nil {
		t.Fatalf("SetSandboxBackend(auto) error = %v", err)
	}
	after := stack.CurrentGateway()
	if after == nil || after == before {
		t.Fatalf("CurrentGateway() after rebuild = %p, before %p; want replacement", after, before)
	}
	if got, err := driver.gateway(); err != nil || got != after {
		t.Fatalf("driver.gateway() after rebuild = %p, %v; want current %p", got, err, after)
	}
}

func TestAllocateSideAgentHandleUsesSharedNamePool(t *testing.T) {
	used := map[string]struct{}{}

	first := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(first) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", first)
	}
	used[first] = struct{}{}
	second := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(second) || second == first {
		t.Fatalf("allocateSideAgentHandle() = %q after %q, want unique shared pool handle", second, first)
	}
	used[second] = struct{}{}
	third := allocateSideAgentHandle(used, "claude")
	if !agenthandle.ContainsPoolName(third) || third == first || third == second {
		t.Fatalf("allocateSideAgentHandle() = %q after %q/%q, want unique shared pool handle", third, first, second)
	}
	if got := allocateSideAgentHandle(used, "anthropic/Claude Agent"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", got)
	}
	if got := allocateSideAgentHandle(used, "!!!"); !agenthandle.ContainsPoolName(got) {
		t.Fatalf("allocateSideAgentHandle() = %q, want shared human-name pool handle", got)
	}
}

func TestGatewayDriverDefersBlankSessionUntilFirstSubmission(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "lazy-session-test",
		StoreDir:       storeDir,
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionID != "" {
		t.Fatalf("Status().SessionID = %q, want empty before first submission", status.SessionID)
	}
	before, err := stack.Gateway.ListSessions(ctx, kernel.ListSessionsRequest{
		AppName:      stack.AppName,
		UserID:       stack.UserID,
		WorkspaceKey: stack.Workspace.Key,
		Limit:        10,
	})
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
	after, err := stack.Gateway.ListSessions(ctx, kernel.ListSessionsRequest{
		AppName:      stack.AppName,
		UserID:       stack.UserID,
		WorkspaceKey: stack.Workspace.Key,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("ListSessions(after) error = %v", err)
	}
	if len(after.Sessions) != 1 {
		t.Fatalf("ListSessions(after) = %d sessions, want one after first submission", len(after.Sessions))
	}
}

func TestGatewayDriverSubmitRoutesActiveSessionInputToActiveTurn(t *testing.T) {
	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []kernel.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       kernel.ActiveTurnKindKernel,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
	}
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		GatewayFn:       func() GatewayService { return gw },
		Workspace:       session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
		SandboxStatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		StartSessionFn: func(context.Context, string, string) (session.Session, error) {
			return activeSession, nil
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
	if gw.beginCalls != 0 {
		t.Fatalf("BeginTurn calls = %d, want 0", gw.beginCalls)
	}
	if got, want := len(gw.activeSubmits), 1; got != want {
		t.Fatalf("active submits = %d, want %d", got, want)
	}
	if got := gw.activeSubmits[0].Text; got != "steer next step" {
		t.Fatalf("active submit text = %q, want trimmed guidance", got)
	}
}

func TestGatewayDriverSubmitDoesNotRouteParticipantActiveTurnInputToActiveTurn(t *testing.T) {
	ctx := context.Background()
	activeSession := session.Session{
		SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "active-session", WorkspaceKey: "ws",
		},
		CWD: t.TempDir(),
	}
	gw := &activeSubmitGatewayService{
		active: []kernel.ActiveTurnState{{
			SessionRef: activeSession.SessionRef,
			Kind:       kernel.ActiveTurnKindParticipant,
			HandleID:   "handle-1",
			RunID:      "run-1",
			TurnID:     "turn-1",
		}},
	}
	driver, err := NewGatewayDriver(ctx, &DriverStack{
		GatewayFn:       func() GatewayService { return gw },
		Workspace:       session.WorkspaceRef{Key: "ws", CWD: activeSession.CWD},
		SandboxStatusFn: func() SandboxStatus { return SandboxStatus{RequestedBackend: "host"} },
		StartSessionFn: func(context.Context, string, string) (session.Session, error) {
			return activeSession, nil
		},
	}, activeSession.SessionID, "surface", "")
	if err != nil {
		t.Fatalf("NewGatewayDriver() error = %v", err)
	}

	_, err = driver.Submit(ctx, Submission{Text: "  main prompt after side run  "})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if got := len(gw.activeSubmits); got != 0 {
		t.Fatalf("active submits = %d, want 0 for participant active turn", got)
	}
	if gw.beginCalls != 1 {
		t.Fatalf("BeginTurn calls = %d, want 1 fallback main turn attempt", gw.beginCalls)
	}
}

func TestGatewayDriverListSessionsSkipsUntitledSessions(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "resume-filter-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.Gateway.StartSession(ctx, kernel.StartSessionRequest{
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,
	}); err != nil {
		t.Fatalf("StartSession(blank) error = %v", err)
	}
	titled, err := stack.Gateway.StartSession(ctx, kernel.StartSessionRequest{
		AppName:   stack.AppName,
		UserID:    stack.UserID,
		Workspace: stack.Workspace,
		Title:     "visible prompt",
	})
	if err != nil {
		t.Fatalf("StartSession(titled) error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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

	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "connect-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "connect-flow-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	if !slashCandidatesHaveValue(xiaomiEndpoints, connectXiaomiAPIBaseURL) {
		t.Fatalf("xiaomi endpoint candidates = %#v, missing api cn", xiaomiEndpoints)
	}
	var foundTokenPlan bool
	for _, item := range xiaomiEndpoints {
		if strings.EqualFold(strings.TrimSpace(item.Value), connectXiaomiTokenPlanCNBaseURL) &&
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "slash-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}

	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "slash-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

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
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{{
			Value:       "claude-sonnet",
			Name:        "Claude Sonnet",
			Description: "remote model",
		}},
		EffortOptions: []gatewayapp.ACPControllerConfigChoice{{
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
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptions: []gatewayapp.ACPControllerConfigChoice{
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
	status := gatewayapp.ACPControllerStatus{
		ModelOptions: []gatewayapp.ACPControllerConfigChoice{
			{Value: "gpt-5.5", Name: "GPT-5.5"},
			{Value: "gpt-5.4", Name: "gpt-5.4"},
		},
		EffortOptionsByModel: map[string][]gatewayapp.ACPControllerConfigChoice{
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "model-reasoning-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "deepseek",
			API:      providers.APIDeepSeek,
			Model:    "deepseek-v4-pro",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "model-reasoning-session", "surface", "deepseek/deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	levels, err := driver.CompleteSlashArg(ctx, "model use deepseek/deepseek-v4-pro", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model use alias) error = %v", err)
	}
	if got := candidateValues(levels); !equalStrings(got, []string{"none", "high", "max"}) {
		t.Fatalf("reasoning candidates = %#v, want none/high/max", levels)
	}
	if _, err := driver.UseModel(ctx, "deepseek/deepseek-v4-pro", "high"); err != nil {
		t.Fatalf("UseModel(reasoning) error = %v", err)
	}
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
	state, err := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SnapshotState() error = %v", err)
	}
	if got := strings.TrimSpace(state[kernel.StateCurrentReasoningEffort].(string)); got != "high" {
		t.Fatalf("reasoning state = %q, want high", got)
	}
	cfg, ok := stack.ModelConfig("deepseek/deepseek-v4-pro")
	if !ok {
		t.Fatal("expected deepseek model config")
	}
	if got := strings.TrimSpace(cfg.ReasoningEffort); got != "high" {
		t.Fatalf("config reasoning effort = %q, want high", got)
	}
}

func TestGatewayDriverConnectPersistsDeepSeekModelDefaults(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "connect-defaults-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "connect-defaults-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "secret",
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if got := status.ContextWindowTokens; got != 1048576 {
		t.Fatalf("status.ContextWindowTokens = %d, want 1048576", got)
	}
	if got := strings.TrimSpace(status.ReasoningEffort); got != "high" {
		t.Fatalf("status.ReasoningEffort = %q, want high", got)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
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
	if cfg.Provider != "" || cfg.BaseURL != "" || cfg.Token != "" || cfg.TokenEnv != "" {
		t.Fatalf("persisted model leaked profile fields: %#v", cfg)
	}
	var conn gatewayapp.ModelProfileConfig
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
	if cfg.MaxOutputTok != 32768 {
		t.Fatalf("persisted max output = %d, want 32768", cfg.MaxOutputTok)
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
		`"reasoning_mode":`,
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "connect-token-env-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "connect-token-env-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-flash",
		APIKey:   "env:DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
	for _, item := range doc.Models.Configs {
		if strings.EqualFold(item.Alias, "deepseek/deepseek-v4-flash") {
			cfg = item
			break
		}
	}
	if cfg.Alias == "" {
		t.Fatalf("persisted configs = %#v, want deepseek/deepseek-v4-flash", doc.Models.Configs)
	}
	var conn gatewayapp.ModelProfileConfig
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "codefree-no-reasoning-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "codefree",
			API:      providers.APICodeFree,
			Model:    "GLM-5.1",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "codefree-no-reasoning-session", "surface", "codefree/glm-5.1")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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

	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "codefree-connect-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "codefree-connect-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	status, err := driver.Connect(ctx, ConnectConfig{
		Provider: "codefree",
		Model:    "GLM-4.7",
	})
	if err != nil {
		t.Fatalf("Connect(codefree) error = %v", err)
	}
	if status.Provider != "codefree" {
		t.Fatalf("provider = %q, want codefree", status.Provider)
	}
	if status.ModelName != "GLM-4.7" {
		t.Fatalf("model name = %q, want GLM-4.7", status.ModelName)
	}
}

func TestGatewayDriverStatusIncludesContextUsageSnapshot(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "status-usage-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider:            "ollama",
			API:                 providers.APIOllama,
			Model:               "llama3",
			ContextWindowTokens: 88000,
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "status-usage-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleUser, "hello")),
			Text:    "hello",
		},
	}); err != nil {
		t.Fatalf("AppendEvent(user) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Message: ptrRuntimeMessage(model.NewTextMessage(model.RoleAssistant, "world")),
			Text:    "world",
			Meta: map[string]any{
				"provider":            "ollama",
				"model":               "llama3",
				"prompt_tokens":       12600,
				"cached_input_tokens": 9000,
				"completion_tokens":   200,
				"reasoning_tokens":    50,
				"total_tokens":        12800,
			},
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
	if status.SessionUsageMain.PromptTokens != 12600 || status.SessionUsageMain.ReasoningTokens != 50 {
		t.Fatalf("main usage = %+v, want assistant usage", status.SessionUsageMain)
	}
}

func TestGatewayDriverSessionTokenUsageDeduplicatesConsecutiveToolCallUsage(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "status-usage-dedupe-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "status-usage-dedupe-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	for _, id := range []string{"call-1", "call-2"} {
		if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: activeSession.SessionRef,
			Event: &session.Event{
				Type: session.EventTypeToolCall,
				Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolCall),
					ToolCallID:    id,
					Kind:          "BASH",
					Title:         "BASH",
					Status:        "pending",
					RawInput:      map[string]any{"cmd": "pwd"},
				}},
				Meta: modelUsageMetaForRuntimeTest(10, 3, 2, 12),
			},
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", id, err)
		}
	}

	usage, err := driver.sessionTokenUsage(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("sessionTokenUsage() error = %v", err)
	}
	if usage.PromptTokens != 10 || usage.CachedInputTokens != 3 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want one model response counted once", usage)
	}
}

func TestGatewayDriverSessionTokenUsageBreakdownIncludesSelfSubagentAndAutoReview(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "status-usage-breakdown-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "status-usage-breakdown-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	activeSession, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected active session")
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: activeSession.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeAssistant,
			Text: "main answer",
			Meta: modelUsageMetaForRuntimeTest(10, 3, 2, 12, 1),
		},
	}); err != nil {
		t.Fatalf("AppendEvent(main) error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		next[kernel.StateUsageAccounting] = map[string]any{
			"auto_review": map[string]any{
				"prompt_tokens":       7,
				"cached_input_tokens": 1,
				"completion_tokens":   2,
				"reasoning_tokens":    2,
				"total_tokens":        9,
			},
		}
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState(auto-review usage) error = %v", err)
	}
	child, err := stack.Sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            activeSession.AppName,
		UserID:             activeSession.UserID,
		Workspace:          session.WorkspaceRef{Key: activeSession.WorkspaceKey, CWD: activeSession.CWD},
		PreferredSessionID: "child-self-usage",
	})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "self-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			AgentName:    "self",
			SessionID:    child.SessionID,
			DelegationID: "task-1",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(self) error = %v", err)
	}
	if _, err := stack.Sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: child.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeAssistant,
			Text: "child answer",
			Meta: modelUsageMetaForRuntimeTest(20, 4, 6, 26, 5),
		},
	}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}

	usage, err := driver.sessionTokenUsageBreakdown(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("sessionTokenUsageBreakdown() error = %v", err)
	}
	if usage.Main.PromptTokens != 10 || usage.Main.ReasoningTokens != 1 || usage.Main.TotalTokens != 12 {
		t.Fatalf("main usage = %+v, want parent model usage", usage.Main)
	}
	if usage.Subagents.PromptTokens != 20 || usage.Subagents.ReasoningTokens != 5 || usage.Subagents.TotalTokens != 26 {
		t.Fatalf("subagent usage = %+v, want self child usage", usage.Subagents)
	}
	if usage.AutoReview.PromptTokens != 7 || usage.AutoReview.ReasoningTokens != 2 || usage.AutoReview.TotalTokens != 9 {
		t.Fatalf("auto-review usage = %+v, want review usage", usage.AutoReview)
	}
	if usage.Total.PromptTokens != 37 || usage.Total.CachedInputTokens != 8 || usage.Total.CompletionTokens != 10 || usage.Total.ReasoningTokens != 8 || usage.Total.TotalTokens != 47 {
		t.Fatalf("total usage = %+v, want all buckets", usage.Total)
	}
}

func TestGatewayDriverDeleteModelRemovesConfiguredAlias(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "slash-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "delete-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "alt-model",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "ollama/alt-model"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "delete-only-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "delete-only-model-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "ollama",
		Model:    "llama3",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "ollama/llama3"); err != nil {
		t.Fatalf("DeleteModel() error = %v", err)
	}
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
	if strings.TrimSpace(status.Model) != "" {
		t.Fatalf("status model = %q, want empty after deleting only model", status.Model)
	}
}

func TestGatewayDriverUseModelResolvesCaseInsensitiveAlias(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "use-model-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "use-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	status, err := driver.UseModel(ctx, "minimax/minimax-m2.7-highspeed")
	if err != nil {
		t.Fatalf("UseModel() error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.Model)); got != "minimax/minimax-m2.7-highspeed" {
		t.Fatalf("status model = %q, want minimax/minimax-m2.7-highspeed", status.Model)
	}
}

func TestGatewayDriverAgentRegistryAndControllerUse(t *testing.T) {
	ctx := context.Background()
	repo := repoRootForGatewayDriverTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "agent-driver-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "copilot",
				Description: "ACP sidecar agent.",
				Command:     "go",
				Args:        []string{"run", "./internal/acpe2eagent"},
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_STUB_REPLY":   "driver acp ok",
					"SDK_ACP_SESSION_ROOT": filepath.Join(root, "agent-sessions"),
					"SDK_ACP_TASK_ROOT":    filepath.Join(root, "agent-tasks"),
				},
			}},
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "agent-driver-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	for _, want := range []string{"claude", "codex", "copilot", "gemini"} {
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
	for _, notInstallable := range []string{"copilot", "gemini"} {
		if slashCandidatesHaveValue(installCandidates, notInstallable) {
			t.Fatalf("agent install candidates = %#v, want no %q", installCandidates, notInstallable)
		}
	}

	status, err := driver.AddAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("AddAgent() error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("AddAgent() status = %#v, want no session participants", status)
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

	status, err = driver.HandoffAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("HandoffAgent(copilot) error = %v", err)
	}
	if got := strings.ToLower(strings.TrimSpace(status.ControllerKind)); got != "acp" {
		t.Fatalf("controller kind after ACP handoff = %q, want acp", status.ControllerKind)
	}

	if _, err := driver.RemoveAgent(ctx, "copilot"); err == nil {
		t.Fatal("RemoveAgent(active copilot) error = nil, want use local first")
	}
	status, err = driver.HandoffAgent(ctx, "local")
	if err != nil {
		t.Fatalf("HandoffAgent(local) error = %v", err)
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

	status, err = driver.RemoveAgent(ctx, "copilot")
	if err != nil {
		t.Fatalf("RemoveAgent(copilot) error = %v", err)
	}
	if len(status.Participants) != 0 {
		t.Fatalf("RemoveAgent() status = %#v, want zero participants", status)
	}
	agents, err = driver.ListAgents(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgents(after remove) error = %v", err)
	}
	if agentCandidatesHaveName(agents, "copilot") {
		t.Fatalf("ListAgents(after remove) = %#v, want copilot removed", agents)
	}
}

func TestGatewayDriverStartAgentSubagentRollsBackAttachmentOnPromptConflict(t *testing.T) {
	ctx := context.Background()
	repo := repoRootForGatewayDriverTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	agentBin := filepath.Join(os.TempDir(), testenv.ExecutableName(fmt.Sprintf("caelis-e2eagent-%d", time.Now().UnixNano())))
	t.Cleanup(func() { _ = os.Remove(agentBin) })
	build := exec.Command("go", "build", "-o", agentBin, "./internal/acpe2eagent")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build e2eagent error = %v\n%s", err, string(output))
	}
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "agent-conflict-rollback-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly: assembly.ResolvedAssembly{
			Agents: []assembly.AgentConfig{{
				Name:        "copilot",
				Description: "ACP sidecar agent.",
				Command:     agentBin,
				WorkDir:     repo,
				Env: map[string]string{
					"SDK_ACP_STUB_REPLY":    "slow sidecar",
					"SDK_ACP_STUB_DELAY_MS": "2000",
					"SDK_ACP_SESSION_ROOT":  filepath.Join(root, "agent-sessions"),
					"SDK_ACP_TASK_ROOT":     filepath.Join(root, "agent-tasks"),
				},
			}},
		},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "agent-conflict-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	first, err := driver.StartAgentSubagent(ctx, "copilot", "first prompt")
	if err != nil {
		t.Fatalf("StartAgentSubagent(first) error = %v", err)
	}
	defer func() {
		closeGatewayDriverTestTurn(t, first)
		if runtime.GOOS == "windows" {
			time.Sleep(500 * time.Millisecond)
		}
	}()

	_, err = driver.StartAgentSubagent(ctx, "copilot", "second prompt")
	if err == nil {
		t.Fatal("StartAgentSubagent(second) error = nil, want active run conflict")
	}
	var gwErr *kernel.Error
	if !kernel.As(err, &gwErr) || gwErr.Code != kernel.CodeActiveRunConflict {
		t.Fatalf("StartAgentSubagent(second) error = %v, want active run conflict", err)
	}
	status, err := driver.AgentStatus(ctx)
	if err != nil {
		t.Fatalf("AgentStatus() error = %v", err)
	}
	if len(status.Participants) != 1 {
		t.Fatalf("AgentStatus().Participants = %#v, want only first sidecar after rollback", status.Participants)
	}
	if status.Participants[0].AgentName != "copilot" || !agenthandle.ContainsPoolName(strings.TrimPrefix(status.Participants[0].Label, "@")) {
		t.Fatalf("remaining participant = %#v, want original copilot sidecar with shared pool label", status.Participants[0])
	}
}

func TestGatewayDriverStatusUsesPersistedDefaultAliasOnStartup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "status-startup-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	if _, err := stack.Connect(gatewayapp.ModelConfig{
		Provider: "deepseek",
		API:      providers.APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Token:    "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	reloaded, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "status-startup-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack(reloaded) error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, reloaded, "startup-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got := strings.TrimSpace(status.Model); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("status model = %q, want deepseek/deepseek-v4-pro", status.Model)
	}
}

func TestGatewayDriverStartupUsesRequestedSessionID(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "lazy-session-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "binding-reset-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	stale, err := stack.StartSession(ctx, "stale-session", "surface")
	if err != nil {
		t.Fatalf("StartSession(stale) error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	current, ok := stack.Gateway.CurrentSession("surface")
	if !ok {
		t.Fatal("expected surface binding to exist after startup")
	}
	if current.SessionID != status.SessionID {
		t.Fatalf("current binding session = %q, want %q", current.SessionID, status.SessionID)
	}
}

func TestGatewayDriverStartupReusesExistingRequestedSession(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "startup-resume-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	existing, err := stack.StartSession(ctx, "sticky-session", "other-surface")
	if err != nil {
		t.Fatalf("StartSession(sticky-session) error = %v", err)
	}

	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "lazy-session-mode-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "sticky-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	startup, ok := driver.currentSession()
	if !ok {
		t.Fatal("expected startup session")
	}
	status, err := driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode() error = %v", err)
	}
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

func TestGatewayDriverSetSessionModeUpdatesLocalApprovalModeUnderACPController(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "acp-approval-mode-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "acp-approval-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "codex",
			Label:           "Codex ACP",
			RemoteSessionID: "remote-1",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	driver := &GatewayDriver{
		stack:               gatewayAppStackForRuntimeTest(stack),
		session:             activeSession,
		hasSession:          true,
		bindingKey:          "surface",
		defaultSessionMode:  "auto-review",
		sessionMode:         "auto-review",
		defaultSandboxType:  "host",
		sandboxType:         "host",
		streamSubscriptions: map[string]struct{}{},
	}

	status, err := driver.SetSessionMode(ctx, "manual")
	if err != nil {
		t.Fatalf("SetSessionMode(manual) error = %v", err)
	}
	if status.SessionMode != "manual" {
		t.Fatalf("status.SessionMode = %q, want manual", status.SessionMode)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("state.SessionMode = %q, want manual", state.SessionMode)
	}
	status, err = driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.SessionMode != "manual" {
		t.Fatalf("Status().SessionMode = %q, want manual", status.SessionMode)
	}
}

func TestGatewayDriverCycleSessionModeUpdatesLocalApprovalModeUnderACPController(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "acp-cycle-approval-mode-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	activeSession, err := stack.StartSession(ctx, "acp-cycle-approval-session", "surface")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	activeSession, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "codex",
			Label:           "Codex ACP",
			RemoteSessionID: "remote-1",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	driver := &GatewayDriver{
		stack:               gatewayAppStackForRuntimeTest(stack),
		session:             activeSession,
		hasSession:          true,
		bindingKey:          "surface",
		defaultSessionMode:  "auto-review",
		sessionMode:         "auto-review",
		defaultSandboxType:  "host",
		sandboxType:         "host",
		streamSubscriptions: map[string]struct{}{},
	}

	status, err := driver.CycleSessionMode(ctx)
	if err != nil {
		t.Fatalf("CycleSessionMode() error = %v", err)
	}
	if status.SessionMode != "manual" {
		t.Fatalf("status.SessionMode = %q, want manual", status.SessionMode)
	}
	state, err := stack.SessionRuntimeState(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatalf("SessionRuntimeState() error = %v", err)
	}
	if state.SessionMode != "manual" {
		t.Fatalf("state.SessionMode = %q, want manual", state.SessionMode)
	}
}

func TestNextACPControllerModeUsesDeclaredModeOrder(t *testing.T) {
	status := gatewayapp.ACPControllerStatus{
		Mode: "default",
		ModeOptions: []gatewayapp.ACPControllerMode{
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
	status := gatewayapp.ACPControllerStatus{
		Mode: "review",
		ModeOptions: []gatewayapp.ACPControllerMode{
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

func TestGatewayDriverACPStatusKeepsAgentFallbackWithoutRemoteModel(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "acp-model-fallback-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
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
	activeSession, err = stack.Sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "codex",
			Label:           "Codex ACP",
			RemoteSessionID: "remote-1",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}

	driver := &GatewayDriver{
		stack:               gatewayAppStackForRuntimeTest(stack),
		session:             activeSession,
		hasSession:          true,
		bindingKey:          "surface",
		defaultSessionMode:  "default",
		sessionMode:         "default",
		defaultSandboxType:  "host",
		sandboxType:         "host",
		streamSubscriptions: map[string]struct{}{},
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "stale-session-alias-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "stale-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	activeSession, err := driver.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, activeSession.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
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
	if got := strings.TrimSpace(status.Model); got != "" {
		t.Fatalf("status model = %q, want empty because alias is stale", status.Model)
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "prefix-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "prefix-model-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	modelActions, err := driver.CompleteSlashArg(ctx, "model", "de", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(model, de) error = %v", err)
	}
	if len(modelActions) != 1 || modelActions[0].Value != "del" {
		t.Fatalf("model action candidates = %#v, want only del", modelActions)
	}

	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		TokenEnv: "DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
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

func TestGatewayDriverCompleteSlashArgAgentRootOrder(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "agent-root-order-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "agent-root-order-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	candidates, err := driver.CompleteSlashArg(ctx, "agent", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(agent) error = %v", err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.Value)
	}
	want := []string{"use", "add", "install", "list", "remove"}
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "agent-install-cancel-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "agent-install-cancel-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := driver.AddAgentWithOptions(ctx, "claude", AgentAddOptions{Install: true})
		done <- err
	}()

	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("AddAgentWithOptions returned before fake npm started: %v", err)
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
			t.Fatalf("AddAgentWithOptions error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddAgentWithOptions did not return after Interrupt")
	}
}

func TestGatewayDriverConnectPersistsMultipleProviders(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "multi-provider-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "multi-provider-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "minimax-secret",
	}); err != nil {
		t.Fatalf("Connect(minimax) error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		APIKey:   "deepseek-secret",
	}); err != nil {
		t.Fatalf("Connect(deepseek) error = %v", err)
	}
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

func TestFindProviderTemplateSupportsOpenAICompatible(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate("openai-compatible")
	if !ok {
		t.Fatal("findProviderTemplate(openai-compatible) = false, want true")
	}
	if tpl.provider != "openai-compatible" {
		t.Fatalf("provider = %q, want openai-compatible", tpl.provider)
	}
	if tpl.defaultBaseURL == "" {
		t.Fatal("defaultBaseURL = empty, want non-empty")
	}
}

func TestFindProviderTemplateSupportsXiaomiTokenPlanCN(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate(connectXiaomiTokenPlanCNAlias)
	if !ok {
		t.Fatalf("findProviderTemplate(%q) = false, want true", connectXiaomiTokenPlanCNAlias)
	}
	if tpl.provider != "xiaomi" {
		t.Fatalf("provider = %q, want xiaomi", tpl.provider)
	}
	if tpl.api != providers.APIMimo {
		t.Fatalf("api = %q, want %q", tpl.api, providers.APIMimo)
	}
	if tpl.defaultBaseURL != connectXiaomiTokenPlanCNBaseURL {
		t.Fatalf("defaultBaseURL = %q, want %q", tpl.defaultBaseURL, connectXiaomiTokenPlanCNBaseURL)
	}
}

func TestFindProviderTemplateRejectsMimoProviderAliases(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"mimo", "mimo-token-plan-cn"} {
		if tpl, ok := findProviderTemplate(provider); ok {
			t.Fatalf("findProviderTemplate(%q) = %#v, want unsupported", provider, tpl)
		}
	}
}

func TestValidateConnectConfigXiaomiTokenPlanCNUsesTokenPlanEnvHint(t *testing.T) {
	t.Parallel()

	tpl, ok := findProviderTemplate("xiaomi")
	if !ok {
		t.Fatal("findProviderTemplate(xiaomi) = false, want true")
	}
	err := validateConnectConfig(tpl, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiTokenPlanCNBaseURL,
	})
	if err == nil || !strings.Contains(err.Error(), "env:MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("validateConnectConfig() error = %v, want MIMO_TOKEN_PLAN_API_KEY hint", err)
	}
}

func TestGatewayDriverConnectXiaomiTokenPlanCNStoresXiaomiProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "xiaomi-token-plan-connect-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "xiaomi-token-plan-connect-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiTokenPlanCNBaseURL,
		APIKey:   "env:MIMO_TOKEN_PLAN_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	doc, err := gatewayapp.LoadAppConfig(root)
	if err != nil {
		t.Fatalf("LoadAppConfig() error = %v", err)
	}
	var cfg gatewayapp.ModelConfig
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
	if cfg.Provider != "" || cfg.BaseURL != "" || cfg.Token != "" || cfg.TokenEnv != "" {
		t.Fatalf("persisted model leaked profile fields: %#v", cfg)
	}
	var profile gatewayapp.ModelProfileConfig
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
	if profile.BaseURL != connectXiaomiTokenPlanCNBaseURL {
		t.Fatalf("profile base_url = %q, want %q", profile.BaseURL, connectXiaomiTokenPlanCNBaseURL)
	}
	if profile.TokenEnv != "MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("profile token_env = %q, want MIMO_TOKEN_PLAN_API_KEY", profile.TokenEnv)
	}
}

func TestGatewayDriverConnectXiaomiEndpointsCoexistUnderVisibleAlias(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "xiaomi-endpoint-coexist-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "xiaomi-endpoint-coexist-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	for _, cfg := range []ConnectConfig{
		{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: connectXiaomiAPIBaseURL, APIKey: "env:XIAOMI_API_KEY"},
		{Provider: "xiaomi", Model: "mimo-v2.5-pro", BaseURL: connectXiaomiTokenPlanCNBaseURL, APIKey: "env:MIMO_TOKEN_PLAN_API_KEY"},
	} {
		if _, err := driver.Connect(ctx, cfg); err != nil {
			t.Fatalf("Connect(%s) error = %v", cfg.BaseURL, err)
		}
	}

	doc, err := gatewayapp.LoadAppConfig(root)
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
	if _, err := driver.UseModel(ctx, "xiaomi/mimo-v2.5-pro"); err == nil || !strings.Contains(err.Error(), "ambiguous model alias") {
		t.Fatalf("UseModel(visible alias) error = %v, want ambiguity", err)
	}
	if _, err := driver.UseModel(ctx, tokenPlanCandidate.Value); err != nil {
		t.Fatalf("UseModel(token-plan hidden id) error = %v", err)
	}
}

func TestGatewayDriverConnectReusesExistingEndpointAuth(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "connect-reuse-auth-test",
		StoreDir:       root,
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "connect-reuse-auth-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2.5-pro",
		BaseURL:  connectXiaomiAPIBaseURL,
		APIKey:   "env:XIAOMI_API_KEY",
	}); err != nil {
		t.Fatalf("Connect(first model) error = %v", err)
	}
	endpoints, err := driver.CompleteSlashArg(ctx, "connect-baseurl:xiaomi", "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-baseurl:xiaomi) error = %v", err)
	}
	var foundReusable bool
	for _, endpoint := range endpoints {
		if endpoint.Value == connectXiaomiAPIBaseURL && endpoint.NoAuth && strings.Contains(endpoint.Detail, "configured auth") {
			foundReusable = true
			break
		}
	}
	if !foundReusable {
		t.Fatalf("endpoint candidates = %#v, want reusable auth marker for api cn", endpoints)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "xiaomi",
		Model:    "mimo-v2-pro",
		BaseURL:  connectXiaomiAPIBaseURL,
	}); err != nil {
		t.Fatalf("Connect(second model without key) error = %v", err)
	}
	doc, err := gatewayapp.LoadAppConfig(root)
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

func TestConnectDefaultsForConfigOpenAICompatibleCustomBaseURL(t *testing.T) {
	t.Parallel()

	defaults, err := connectDefaultsForConfig(context.Background(), ConnectConfig{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://proxy.example.test/v1",
	})
	if err != nil {
		t.Fatalf("connectDefaultsForConfig() error = %v", err)
	}
	if defaults.ContextWindow <= 0 {
		t.Fatalf("ContextWindow = %d, want > 0", defaults.ContextWindow)
	}
	if defaults.MaxOutput <= 0 {
		t.Fatalf("MaxOutput = %d, want > 0", defaults.MaxOutput)
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

	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "file-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "file-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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

	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "skill-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "skill-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "mention-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "mention-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	activeSession, err := driver.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession() error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "side-1",
			Kind:         session.ParticipantKindACP,
			Role:         session.ParticipantRoleSidecar,
			AgentName:    "codex",
			Label:        "@jeff",
			SessionID:    "child-1",
			Source:       "custom_codex",
			DelegationID: "task-side",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "legacy-side-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleSidecar,
			AgentName:    "legacy",
			Label:        "@jill",
			SessionID:    "legacy-child-1",
			DelegationID: "task-legacy",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(legacy-side) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "task-1",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			Label:        "@jude",
			SessionID:    "child-2",
			DelegationID: "task-1",
		},
	}); err != nil {
		t.Fatalf("PutParticipant(delegated) error = %v", err)
	}
	if _, err := stack.Sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:           "self-001",
			Kind:         session.ParticipantKindSubagent,
			Role:         session.ParticipantRoleDelegated,
			AgentName:    "self",
			Label:        "@jude",
			SessionID:    "self-child-1",
			DelegationID: "task-self",
		},
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
	if len(status.Participants) != 2 || status.Participants[0].ID != "side-1" || status.Participants[1].ID != "legacy-side-1" {
		t.Fatalf("AgentStatus().Participants = %#v, want visible side participants", status.Participants)
	}
	if len(status.DelegatedParticipants) != 2 || status.DelegatedParticipants[0].ID != "task-1" || status.DelegatedParticipants[1].ID != "self-001" {
		t.Fatalf("AgentStatus().DelegatedParticipants = %#v, want delegated task summary", status.DelegatedParticipants)
	}
}

func TestGatewayDriverCompleteResumeIncludesMetadataAndRecentFirst(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "resume-complete-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   workspace,
		WorkspaceCWD:   workspace,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	first, err := stack.Gateway.StartSession(ctx, kernel.StartSessionRequest{
		AppName:    stack.AppName,
		UserID:     stack.UserID,
		Workspace:  stack.Workspace,
		Title:      "First Task",
		BindingKey: "first-binding",
	})
	if err != nil {
		t.Fatalf("StartSession(first) error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, first.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		next[kernel.StateCurrentModelAlias] = "openai/gpt-4o-mini"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState(first) error = %v", err)
	}
	second, err := stack.Gateway.StartSession(ctx, kernel.StartSessionRequest{
		AppName:    stack.AppName,
		UserID:     stack.UserID,
		Workspace:  stack.Workspace,
		Title:      "Second Task",
		BindingKey: "second-binding",
	})
	if err != nil {
		t.Fatalf("StartSession(second) error = %v", err)
	}
	if err := stack.Sessions.UpdateState(ctx, second.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		next[kernel.StateCurrentModelAlias] = "deepseek/deepseek-v4-flash"
		return next, nil
	}); err != nil {
		t.Fatalf("UpdateState(second) error = %v", err)
	}

	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "resume-complete-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "delete-unknown-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "delete-unknown-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if err := driver.DeleteModel(ctx, "minimax/minimax-m1"); err == nil {
		t.Fatal("DeleteModel() error = nil, want unknown alias error")
	}
}

func TestGatewayDriverConnectModelCandidatesIncludeConfiguredProviderModels(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "connect-candidates-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
		Model: gatewayapp.ModelConfig{
			Provider: "ollama",
			API:      providers.APIOllama,
			Model:    "llama3",
		},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "connect-candidates-session", "surface", "ollama/llama3")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		APIKey:   "secret",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

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
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "missing-key-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "missing-key-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "openai",
		Model:    "gpt-4o",
	}); err == nil || !strings.Contains(err.Error(), "env:OPENAI_API_KEY") {
		t.Fatalf("Connect() error = %v, want actionable env hint", err)
	}
}

func TestGatewayDriverConnectRejectsInvalidBaseURL(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "invalid-baseurl-test",
		StoreDir:       t.TempDir(),
		WorkspaceKey:   t.TempDir(),
		WorkspaceCWD:   t.TempDir(),
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "invalid-baseurl-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "openai-compatible",
		Model:    "gpt-4o",
		BaseURL:  "not-a-url",
		APIKey:   "secret",
	}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "base url is invalid") {
		t.Fatalf("Connect() error = %v, want invalid base URL guidance", err)
	}
}

func TestGatewayDriverStatusIncludesDoctorDiagnostics(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	workdir := t.TempDir()
	stack, err := newGatewayDriverTestStack(t, gatewayapp.Config{
		AppName:        "caelis",
		UserID:         "doctor-status-test",
		StoreDir:       root,
		WorkspaceKey:   workdir,
		WorkspaceCWD:   workdir,
		PermissionMode: "default",
		Assembly:       assembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() error = %v", err)
	}
	driver, err := newGatewayDriverFromGatewayAppStack(ctx, stack, "doctor-status-session", "surface", "")
	if err != nil {
		t.Fatalf("newGatewayDriverFromGatewayAppStack() error = %v", err)
	}
	if _, err := driver.Connect(ctx, ConnectConfig{
		Provider: "minimax",
		Model:    "MiniMax-M2.7-highspeed",
		TokenEnv: "MINIMAX_API_KEY",
	}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if _, err := driver.SetSessionMode(ctx, "manual"); err != nil {
		t.Fatalf("SetSessionMode() error = %v", err)
	}
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
	activeSession := session.Session{SessionRef: session.SessionRef{SessionID: "grant-session"}}
	driver := &GatewayDriver{
		stack: &DriverStack{
			Workspace: session.WorkspaceRef{CWD: "/workspace"},
			DoctorFn: func(context.Context, DoctorRequest) (DoctorReport, error) {
				return DoctorReport{
					SessionID:                "grant-session",
					PermissionGrantCount:     2,
					PermissionGrantNetwork:   true,
					PermissionReadRootCount:  3,
					PermissionWriteRootCount: 1,
				}, nil
			},
		},
		session:    activeSession,
		hasSession: true,
	}
	status, err := driver.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.PermissionGrantCount != 2 || !status.PermissionGrantNetwork || status.PermissionReadRootCount != 3 || status.PermissionWriteRootCount != 1 {
		t.Fatalf("permission grant summary = count:%d network:%v read:%d write:%d, want 2/true/3/1", status.PermissionGrantCount, status.PermissionGrantNetwork, status.PermissionReadRootCount, status.PermissionWriteRootCount)
	}
}

type activeSubmitGatewayService struct {
	active        []kernel.ActiveTurnState
	activeSubmits []kernel.SubmitActiveTurnRequest
	beginCalls    int
}

func (g *activeSubmitGatewayService) Streams() stream.Service { return nil }

func (g *activeSubmitGatewayService) BeginTurn(context.Context, kernel.BeginTurnRequest) (kernel.BeginTurnResult, error) {
	g.beginCalls++
	return kernel.BeginTurnResult{}, nil
}

func (g *activeSubmitGatewayService) SubmitActiveTurn(_ context.Context, req kernel.SubmitActiveTurnRequest) error {
	g.activeSubmits = append(g.activeSubmits, req)
	return nil
}

func (g *activeSubmitGatewayService) Interrupt(context.Context, kernel.InterruptRequest) error {
	return nil
}

func (g *activeSubmitGatewayService) ResumeSession(context.Context, kernel.ResumeSessionRequest) (session.LoadedSession, error) {
	return session.LoadedSession{}, nil
}

func (g *activeSubmitGatewayService) ListSessions(context.Context, kernel.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}

func (g *activeSubmitGatewayService) ReplayEvents(context.Context, kernel.ReplayEventsRequest) (kernel.ReplayEventsResult, error) {
	return kernel.ReplayEventsResult{}, nil
}

func (g *activeSubmitGatewayService) ControlPlaneState(context.Context, kernel.ControlPlaneStateRequest) (kernel.ControlPlaneState, error) {
	return kernel.ControlPlaneState{}, nil
}

func (g *activeSubmitGatewayService) HandoffController(context.Context, kernel.HandoffControllerRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) AttachParticipant(context.Context, kernel.AttachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) PromptParticipant(context.Context, kernel.PromptParticipantRequest) (kernel.BeginTurnResult, error) {
	return kernel.BeginTurnResult{}, nil
}

func (g *activeSubmitGatewayService) DetachParticipant(context.Context, kernel.DetachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func (g *activeSubmitGatewayService) ActiveTurns() []kernel.ActiveTurnState {
	return append([]kernel.ActiveTurnState(nil), g.active...)
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
