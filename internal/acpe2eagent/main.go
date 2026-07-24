package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assemblyapi "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/protocol/acp"
	acptaskstream "github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func main() {
	llm, err := resolveLLM()
	if err != nil {
		log.Fatal(err)
	}
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            sessionRootDir(),
		SessionIDGenerator: newSessionID,
	})
	sessions := sessionStore
	taskStore := sessionfile.NewTaskStore(sessionStore)
	assembly, err := resolveAssembly()
	if err != nil {
		log.Fatal(err)
	}
	modeProvider, configProvider := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		Assembly: assembly,
		Sessions: sessions,
		AppName:  "caelis",
		UserID:   "acp",
	})
	contextRouter, err := controlplane.NewContextRouter(sessions)
	if err != nil {
		log.Fatal(err)
	}
	localCfg := runtime.Config{
		Sessions:                 sessions,
		TaskStore:                taskStore,
		ControllerEventForwarder: acpbridge.NewControllerForwarder(sessions),
		ControllerContextRouter:  contextRouter,
		AgentFactory: chat.Factory{
			SystemPrompt: strings.TrimSpace(os.Getenv("SDK_ACP_SYSTEM_PROMPT")),
		},
	}
	if len(assembly.Agents) > 0 {
		controlPlane, cpErr := bridgeassembly.NewControlPlane(bridgeassembly.ControlPlaneConfig{
			Agents: assembly.Agents,
		})
		if cpErr != nil {
			log.Fatal(cpErr)
		}
		localCfg.Controllers = controlPlane.Controllers
		localCfg.Subagents = controlPlane.Subagents
	}
	controlCoordinator, err := controlplane.NewCoordinator(controlplane.CoordinatorConfig{
		Sessions: sessions, Controllers: localCfg.Controllers, Context: contextRouter,
	})
	if err != nil {
		log.Fatal(err)
	}
	localCfg.ControllerRecovery = controlCoordinator
	rt, err := runtime.New(localCfg)
	if err != nil {
		log.Fatal(err)
	}
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks:      taskStore,
		Streams:    rt.Streams,
		Authorizer: acpe2eTaskStreamAuthorizer{sessions: sessions},
		Secret:     []byte("caelis-acpe2e-taskstream-secret-v1"),
	})
	if err != nil {
		log.Fatal(err)
	}
	agent, err := runtimeacp.New(runtimeacp.Config{
		Runtime:  rt,
		Sessions: sessions,
		AgentInfo: &acp.Implementation{
			Name:    "caelis-sdk",
			Title:   "Caelis SDK ACP Agent",
			Version: "0.1.0",
		},
		BuildAgentSpec: func(ctx context.Context, active session.Session, _ acp.PromptRequest) (agent.AgentSpec, error) {
			return buildSpec(ctx, active, llm, assembly, modeProvider, configProvider)
		},
		Modes:               modeProvider,
		Config:              configProvider,
		TaskStreams:         acptaskstream.New(controlTaskStreams),
		TaskStreamPrincipal: acptaskstream.Principal{ID: "acp"},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := acp.ServeStdio(context.Background(), agent, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

type acpe2eTaskStreamAuthorizer struct {
	sessions session.Service
}

func (a acpe2eTaskStreamAuthorizer) AuthorizeTaskStream(
	ctx context.Context,
	principal controltaskstream.Principal,
	sessionID string,
) error {
	if strings.TrimSpace(principal.ID) != "acp" {
		return fmt.Errorf("acpe2eagent: task stream principal is not authorized")
	}
	if a.sessions == nil {
		return fmt.Errorf("acpe2eagent: session service is unavailable")
	}
	_, err := a.sessions.Session(ctx, session.SessionRef{
		AppName:   "caelis",
		UserID:    "acp",
		SessionID: strings.TrimSpace(sessionID),
	})
	return err
}

var _ controltaskstream.Authorizer = acpe2eTaskStreamAuthorizer{}

func resolveLLM() (model.LLM, error) {
	if mode := strings.TrimSpace(os.Getenv("SDK_ACP_SCRIPTED_MODE")); mode != "" {
		switch mode {
		case "async_command":
			return &scriptedAsyncCommandLLM{}, nil
		case "interactive_command":
			return &scriptedInteractiveCommandLLM{}, nil
		case "approval_command":
			return &scriptedApprovalCommandLLM{}, nil
		case "probe_spawn":
			return &scriptedProbeSpawnLLM{}, nil
		case "spawn":
			return &scriptedSpawnLLM{}, nil
		case "spawn_passthrough":
			return &scriptedSpawnPassthroughLLM{}, nil
		case "mode_config":
			return &scriptedModeConfigLLM{}, nil
		default:
			return nil, fmt.Errorf("unknown SDK_ACP_SCRIPTED_MODE %q", mode)
		}
	}
	if reply := strings.TrimSpace(os.Getenv("SDK_ACP_STUB_REPLY")); reply != "" {
		delay := time.Duration(0)
		if raw := strings.TrimSpace(os.Getenv("SDK_ACP_STUB_DELAY_MS")); raw != "" {
			if ms, err := time.ParseDuration(raw + "ms"); err == nil {
				delay = ms
			}
		}
		return staticLLM{text: reply, delay: delay}, nil
	}
	return nil, fmt.Errorf("set SDK_ACP_SCRIPTED_MODE or SDK_ACP_STUB_REPLY for acpe2eagent")
}

func sessionRootDir() string {
	if root := strings.TrimSpace(os.Getenv("SDK_ACP_SESSION_ROOT")); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "caelis-sdk-acp-sessions")
}

func resolveAssembly() (assemblyapi.ResolvedAssembly, error) {
	assembly := assemblyapi.ResolvedAssembly{}

	if root := strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_ROOT")); root != "" {
		assembly.Skills = append(assembly.Skills, assemblyapi.SkillBundle{
			Plugin:    firstNonEmpty(strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_PLUGIN")), "app"),
			Namespace: strings.TrimSpace(os.Getenv("SDK_ACP_SKILLS_NAMESPACE")),
			Root:      root,
			Disabled:  splitCommaList(os.Getenv("SDK_ACP_SKILLS_DISABLED")),
		})
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_MODE_CONFIG")) == "1" {
		assembly.Modes = []assemblyapi.ModeConfig{
			{
				ID:          "default",
				Name:        "Default",
				Description: "Standard coding mode",
				Runtime: assemblyapi.RuntimeOverrides{
					PolicyMode:   "default",
					SystemPrompt: "mode-default-marker",
				},
			},
			{
				ID:          "plan",
				Name:        "Plan",
				Description: "Planning-first mode",
				Runtime: assemblyapi.RuntimeOverrides{
					PolicyMode:   "plan",
					SystemPrompt: "mode-plan-marker",
				},
			},
		}
		assembly.Configs = []assemblyapi.ConfigOption{{
			ID:           "reasoning",
			Name:         "Reasoning",
			Description:  "Reasoning profile",
			DefaultValue: "balanced",
			Options: []assemblyapi.ConfigSelectOption{
				{
					Value: "balanced",
					Name:  "Balanced",
					Runtime: assemblyapi.RuntimeOverrides{
						Reasoning: model.ReasoningConfig{Effort: "medium"},
					},
				},
				{
					Value: "deep",
					Name:  "Deep",
					Runtime: assemblyapi.RuntimeOverrides{
						Reasoning: model.ReasoningConfig{Effort: "high"},
					},
				},
			},
		}}
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_MODEL_CONFIG")) == "1" {
		assembly.Configs = append(assembly.Configs,
			assemblyapi.ConfigOption{
				ID: "model", Name: "Model", Description: "E2E model catalog", DefaultValue: "sonnet",
				Options: []assemblyapi.ConfigSelectOption{
					{Value: "sonnet", Name: "Sonnet"},
					{Value: "opus", Name: "Opus"},
				},
			},
			assemblyapi.ConfigOption{
				ID: "effort", Name: "Reasoning effort", Description: "E2E reasoning default", DefaultValue: "high",
				Options: []assemblyapi.ConfigSelectOption{
					{Value: "high", Name: "High", Runtime: assemblyapi.RuntimeOverrides{Reasoning: model.ReasoningConfig{Effort: "high"}}},
					{Value: "max", Name: "Max", Runtime: assemblyapi.RuntimeOverrides{Reasoning: model.ReasoningConfig{Effort: "max"}}},
				},
			},
		)
	}

	if strings.TrimSpace(os.Getenv("SDK_ACP_CHILD_NO_SPAWN")) == "1" {
		return assembly, nil
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_SPAWN")) != "1" {
		return assembly, nil
	}
	selfCmd := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_CMD"))
	if selfCmd == "" {
		return assembly, nil
	}
	name := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_NAME"))
	if name == "" {
		name = "self"
	}
	assembly.Agents = append(assembly.Agents, assemblyapi.AgentConfig{
		Name:        name,
		Description: strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_DESC")),
		Command:     "bash",
		Args:        []string{"-lc", selfCmd},
	})
	return assembly, nil
}

func newSessionID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return "session-" + hex.EncodeToString(buf[:])
}

type staticLLM struct {
	text  string
	delay time.Duration
}

func (m staticLLM) Name() string { return "static" }

func (m staticLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (m staticLLM) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, m.text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
			},
		}, nil)
	}
}

func buildSpec(
	ctx context.Context,
	active session.Session,
	llm model.LLM,
	assembly assemblyapi.ResolvedAssembly,
	modes acp.ModeProvider,
	configs acp.ConfigProvider,
) (agent.AgentSpec, error) {
	rt, err := host.New(host.Config{CWD: active.CWD})
	if err != nil {
		return agent.AgentSpec{}, err
	}
	tools, err := builtin.BuildCoreTools(builtin.CoreToolsConfig{
		Runtime: rt,
	})
	if err != nil {
		return agent.AgentSpec{}, err
	}
	agents := make([]delegation.Agent, 0, len(assembly.Agents))
	for _, one := range assembly.Agents {
		agents = append(agents, delegation.Agent{
			Name:        strings.TrimSpace(one.Name),
			Description: strings.TrimSpace(one.Description),
		})
	}
	if len(agents) > 0 {
		tools = append(tools, spawn.New(agents))
	}
	metadata, err := selectedAssemblyMetadata(ctx, active, assembly, modes, configs)
	if err != nil {
		return agent.AgentSpec{}, err
	}
	return agent.AgentSpec{
		Name:     "chat",
		Model:    llm,
		Tools:    tools,
		Metadata: metadata,
	}, nil
}

func selectedAssemblyMetadata(
	ctx context.Context,
	active session.Session,
	resolved assemblyapi.ResolvedAssembly,
	modes acp.ModeProvider,
	configs acp.ConfigProvider,
) (map[string]any, error) {
	metadata := map[string]any{}
	if modes != nil {
		state, err := modes.SessionModes(ctx, active)
		if err != nil {
			return nil, err
		}
		if state != nil && strings.TrimSpace(state.CurrentModeID) != "" {
			mode, ok := assemblyapi.LookupMode(resolved, state.CurrentModeID)
			if !ok {
				return nil, fmt.Errorf("acpe2eagent: selected mode %q is not declared", state.CurrentModeID)
			}
			assemblyapi.ApplyRuntimeOverrides(metadata, mode.Runtime)
		}
	}
	if configs != nil {
		options, err := configs.SessionConfigOptions(ctx, active)
		if err != nil {
			return nil, err
		}
		for _, current := range options {
			value, ok := current.CurrentValue.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("acpe2eagent: selected config %q has a non-string value", current.ID)
			}
			option, ok := assemblyapi.LookupConfigSelectOption(resolved, current.ID, value)
			if !ok {
				return nil, fmt.Errorf("acpe2eagent: selected config %q value %q is not declared", current.ID, value)
			}
			assemblyapi.ApplyRuntimeOverrides(metadata, option.Runtime)
		}
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

func splitCommaList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, one := range parts {
		one = strings.TrimSpace(one)
		if one != "" {
			out = append(out, one)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, one := range values {
		if strings.TrimSpace(one) != "" {
			return strings.TrimSpace(one)
		}
	}
	return ""
}

type scriptedAsyncCommandLLM struct {
	calls      int
	taskHandle string
}

type scriptedInteractiveCommandLLM struct {
	calls      int
	taskHandle string
}

type scriptedSpawnLLM struct {
	calls      int
	taskHandle string
}

type scriptedApprovalCommandLLM struct {
	calls      int
	taskHandle string
}

type scriptedProbeSpawnLLM struct{}
type scriptedSpawnPassthroughLLM struct {
	calls      int
	taskHandle string
}
type scriptedModeConfigLLM struct{}

func (m *scriptedSpawnLLM) Name() string { return "scripted-spawn" }

func (m *scriptedSpawnLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskHandle = findTaskHandle(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-1",
						Name: spawn.ToolName,
						Args: string(mustJSON(map[string]any{
							"agent":  "self",
							"prompt": "Reply with exactly: spawn child ok",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-1",
						Name: names.Task,
						Args: string(mustJSON(map[string]any{
							"action": "wait",
							"handle": m.taskHandle,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "spawn child ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedAsyncCommandLLM) Name() string { return "scripted-async-command" }

func (m *scriptedInteractiveCommandLLM) Name() string { return "scripted-interactive-command" }

func (m *scriptedApprovalCommandLLM) Name() string { return "scripted-approval-command" }

func (m *scriptedProbeSpawnLLM) Name() string       { return "scripted-probe-spawn" }
func (m *scriptedSpawnPassthroughLLM) Name() string { return "scripted-spawn-passthrough" }
func (m *scriptedModeConfigLLM) Name() string       { return "scripted-mode-config" }

func (*scriptedAsyncCommandLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedInteractiveCommandLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedApprovalCommandLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedProbeSpawnLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedSpawnLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedSpawnPassthroughLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func (*scriptedModeConfigLLM) Capabilities() model.Capabilities {
	return scriptedModelCapabilities()
}

func scriptedModelCapabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}

func (m *scriptedAsyncCommandLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskHandle = findTaskHandle(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "command-async-1",
						Name: names.RunCommand,
						Args: string(mustJSON(map[string]any{
							"command":       "sleep 0.05; printf 'acpx async command ok'",
							"workdir":       ".",
							"yield_time_ms": 5,
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-1",
						Name: names.Task,
						Args: string(mustJSON(map[string]any{"action": "wait", "handle": m.taskHandle})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "acpx async command ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedInteractiveCommandLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskHandle = findTaskHandle(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		var response model.Response
		response.TurnComplete = true
		response.StepComplete = true
		response.Status = model.ResponseStatusCompleted
		response.FinishReason = model.FinishReasonToolCalls
		switch m.calls {
		case 1:
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "command-interactive-1",
				Name: names.RunCommand,
				Args: string(mustJSON(map[string]any{
					"command":       "printf 'interactive ready\\n'; while IFS= read -r line; do printf 'echo:%s\\n' \"$line\"; sleep 0.25; printf 'later:%s\\n' \"$line\"; done",
					"workdir":       ".",
					"yield_time_ms": 5,
				})),
			}}, "")
		case 2:
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "task-write-interactive-1",
				Name: names.Task,
				Args: string(mustJSON(map[string]any{
					"action": "write",
					"handle": m.taskHandle,
					"input":  "ping",
				})),
			}}, "")
		case 3:
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "task-read-interactive-1",
				Name: names.Task,
				Args: string(mustJSON(map[string]any{
					"action": "read",
					"handle": m.taskHandle,
				})),
			}}, "")
		case 4:
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "task-cancel-interactive-1",
				Name: names.Task,
				Args: string(mustJSON(map[string]any{
					"action": "cancel",
					"handle": m.taskHandle,
				})),
			}}, "")
		default:
			response.Message = model.NewTextMessage(model.RoleAssistant, "acpx interactive command ok")
			response.FinishReason = model.FinishReasonStop
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &response}, nil)
	}
}

func (m *scriptedApprovalCommandLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskHandle = findTaskHandle(req)
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "command-approval-1",
						Name: names.RunCommand,
						Args: string(mustJSON(map[string]any{
							"command":             "printf 'child approval ok\n'; sleep 0.2",
							"workdir":             ".",
							"yield_time_ms":       5,
							"sandbox_permissions": "require_escalated",
							"justification":       "Do you want to run this command outside the sandbox?",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-approval-1",
						Name: names.Task,
						Args: string(mustJSON(map[string]any{"action": "wait", "handle": m.taskHandle})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, "child approval ok"),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedProbeSpawnLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	hasSpawn := false
	if req != nil {
		for _, one := range req.Tools {
			if one.Kind == model.ToolSpecKindFunction && one.Function != nil && strings.EqualFold(strings.TrimSpace(one.Function.Name), spawn.ToolName) {
				hasSpawn = true
				break
			}
		}
	}
	text := "spawn disabled"
	if hasSpawn {
		text = "spawn enabled"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func (m *scriptedSpawnPassthroughLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	if m.calls == 2 {
		m.taskHandle = findTaskHandle(req)
	}
	resultText := strings.TrimSpace(findTaskResult(req))
	if resultText == "" {
		resultText = "spawn child ok"
	}
	return func(yield func(*model.StreamEvent, error) bool) {
		switch m.calls {
		case 1:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "spawn-pass-1",
						Name: spawn.ToolName,
						Args: string(mustJSON(map[string]any{
							"agent":  "self",
							"prompt": "Check whether SPAWN is available and reply with exactly the result.",
						})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		case 2:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message: model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
						ID:   "task-wait-spawn-pass-1",
						Name: names.Task,
						Args: string(mustJSON(map[string]any{"action": "wait", "handle": m.taskHandle})),
					}}, ""),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonToolCalls,
				},
			}, nil)
		default:
			yield(&model.StreamEvent{
				Type: model.StreamEventTurnDone,
				Response: &model.Response{
					Message:      model.NewTextMessage(model.RoleAssistant, resultText),
					TurnComplete: true,
					StepComplete: true,
					Status:       model.ResponseStatusCompleted,
					FinishReason: model.FinishReasonStop,
				},
			}, nil)
		}
	}
}

func (m *scriptedModeConfigLLM) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	mode := "unknown"
	if req != nil {
		instructions := stringifyParts(req.Instructions)
		switch {
		case strings.Contains(instructions, "mode-plan-marker"):
			mode = "plan"
		case strings.Contains(instructions, "mode-default-marker"):
			mode = "default"
		}
	}
	effort := ""
	if req != nil {
		effort = strings.TrimSpace(req.Reasoning.Effort)
	}
	text := fmt.Sprintf("mode=%s effort=%s", mode, firstNonEmpty(effort, "none"))
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{
			Type: model.StreamEventTurnDone,
			Response: &model.Response{
				Message:      model.NewTextMessage(model.RoleAssistant, text),
				TurnComplete: true,
				StepComplete: true,
				Status:       model.ResponseStatusCompleted,
				FinishReason: model.FinishReasonStop,
			},
		}, nil)
	}
}

func stringifyParts(parts []model.Part) string {
	var out []string
	for _, one := range parts {
		if one.Text != nil {
			if text := strings.TrimSpace(one.Text.Text); text != "" {
				out = append(out, text)
			}
			continue
		}
		if one.Reasoning != nil && one.Reasoning.VisibleText != nil {
			if text := strings.TrimSpace(*one.Reasoning.VisibleText); text != "" {
				out = append(out, text)
			}
		}
	}
	return strings.Join(out, "\n")
}

func findTaskHandle(req *model.Request) string {
	if req == nil {
		return ""
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if handle, _ := payload["handle"].(string); strings.TrimSpace(handle) != "" {
					return strings.TrimSpace(handle)
				}
			}
		}
	}
	return ""
}

func findTaskResult(req *model.Request) string {
	if req == nil {
		return ""
	}
	for _, message := range req.Messages {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Kind != model.PartKindJSON || part.JSON == nil {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(part.JSONValue(), &payload); err != nil {
					continue
				}
				if text, _ := payload["result"].(string); strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func mustJSON(value map[string]any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
