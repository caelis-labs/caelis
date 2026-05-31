//go:build e2e

package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/app/services"
	"github.com/OnslaughtSnail/caelis/internal/surface/headless"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestLocalStackACPMainControllerE2E(t *testing.T) {
	root := t.TempDir()
	workdir := t.TempDir()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName:      "caelis",
			UserID:       "user-1",
			WorkspaceKey: "repo",
			WorkspaceCWD: workdir,
			Store: config.Store{
				Backend: "jsonl",
				URI:     filepath.Join(root, "sessions"),
			},
		},
		Provider: localACPStaticProvider{},
		ExternalACPAgents: []acpexternal.Config{{
			AgentID:     "codex",
			AgentName:   "codex",
			Description: "ACP main controller.",
			Command:     os.Args[0],
			Args:        []string{"-test.run=TestLocalStackACPMainControllerHelperProcess", "--"},
			Env:         []string{"CAELIS_TEST_LOCAL_ACP_CONTROLLER_HELPER=1"},
		}},
	})
	if err != nil {
		t.Fatalf("local.New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	active, err := stack.Services().Sessions().Start(ctx, services.StartSessionRequest{
		PreferredSessionID: "gateway-acp-main",
		Workspace: session.Workspace{
			Key: "repo",
			CWD: workdir,
		},
		Title: "surface-acp-main",
	})
	if err != nil {
		t.Fatalf("Sessions.Start() error = %v", err)
	}

	handoff, err := stack.Services().Controllers().Handoff(ctx, services.ControllerHandoffRequest{
		SessionRef: active.Ref,
		Target:     "codex",
		Source:     "test",
		Reason:     "delegate main control",
	})
	if err != nil {
		t.Fatalf("Controllers.Handoff() error = %v", err)
	}
	if handoff.Controller.Kind != session.ControllerACP || strings.TrimSpace(handoff.Controller.EpochID) == "" {
		t.Fatalf("handoff controller = %#v, want active ACP controller with epoch", handoff.Controller)
	}

	status, found, err := stack.Services().Controllers().Status(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Controllers.Status() error = %v", err)
	}
	if !found || status.Agent != "codex" {
		t.Fatalf("controller status = %#v found=%v, want codex", status, found)
	}
	status, err = stack.Services().Controllers().SetMode(ctx, active.Ref, "manual")
	if err != nil {
		t.Fatalf("Controllers.SetMode(manual) error = %v", err)
	}
	if got := strings.TrimSpace(status.Mode); got != "manual" {
		t.Fatalf("controller status mode = %q, want manual", got)
	}

	result, err := headless.RunOnce(ctx, headless.Request{
		Services:           stack.Services(),
		SessionRef:         active.Ref,
		PreferredSessionID: active.SessionID,
		Input:              "run through acp controller",
		Surface:            "headless-acp-main-e2e",
	})
	if err != nil {
		t.Fatalf("headless.RunOnce() error = %v", err)
	}
	if got := strings.TrimSpace(result.Output); got != "gateway acp main ok" {
		t.Fatalf("headless output = %q, want %q", got, "gateway acp main ok")
	}

	loaded, err := stack.Services().Sessions().Load(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Sessions.Load() error = %v", err)
	}
	var sawACPAssistant bool
	for _, event := range loaded.Events {
		if event.Type != session.EventAssistant || event.Scope == nil {
			continue
		}
		if event.Scope.Controller.Kind == session.ControllerACP && strings.TrimSpace(session.EventText(event)) == "gateway acp main ok" {
			sawACPAssistant = true
			break
		}
	}
	if !sawACPAssistant {
		t.Fatalf("loaded events missing ACP-scoped assistant reply: %#v", loaded.Events)
	}

	status, found, err = stack.Services().Controllers().Status(ctx, active.Ref)
	if err != nil {
		t.Fatalf("Controllers.Status(after turn) error = %v", err)
	}
	if !found || status.RemoteSessionID != "remote-controller-session" || status.Mode != "manual" || len(status.ModeOptions) != 2 {
		t.Fatalf("controller status after turn = %#v found=%v, want remote session and propagated mode options", status, found)
	}
}

func TestLocalStackACPMainControllerHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_TEST_LOCAL_ACP_CONTROLLER_HELPER") != "1" {
		return
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	currentMode := "default"
	err := conn.Serve(context.Background(), func(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case schema.MethodInitialize:
			return schema.InitializeResponse{
				ProtocolVersion: schema.CurrentProtocolVersion,
				AgentInfo:       &schema.Implementation{Name: "local-acp-controller-helper", Version: "test"},
			}, nil
		case schema.MethodSessionNew:
			return schema.NewSessionResponse{
				SessionID:     "remote-controller-session",
				ConfigOptions: controllerHelperConfigOptions(currentMode),
			}, nil
		case schema.MethodSessionResume:
			return schema.ResumeSessionResponse{ConfigOptions: controllerHelperConfigOptions(currentMode)}, nil
		case schema.MethodSessionSetConfig:
			var req schema.SetSessionConfigOptionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if strings.TrimSpace(req.ConfigID) == "mode" {
				if text, ok := req.Value.(string); ok && strings.TrimSpace(text) != "" {
					currentMode = strings.TrimSpace(text)
				}
			}
			return schema.SetSessionConfigOptionResponse{ConfigOptions: controllerHelperConfigOptions(currentMode)}, nil
		case schema.MethodSessionPrompt:
			var req schema.PromptRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "gateway acp main ok"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func controllerHelperConfigOptions(mode string) []schema.SessionConfigOption {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "default"
	}
	return []schema.SessionConfigOption{{
		Type:         "select",
		ID:           "mode",
		Name:         "Mode",
		Category:     "mode",
		CurrentValue: mode,
		Options: []schema.SessionConfigSelectOption{
			{Value: "default", Name: "Default"},
			{Value: "manual", Name: "Manual"},
		},
	}}
}

type localACPStaticProvider struct{}

func (localACPStaticProvider) ID() string {
	return "local-acp-static"
}

func (localACPStaticProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "local-acp-static", Provider: "test"}}, nil
}

func (localACPStaticProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &model.StaticStream{}, nil
}
