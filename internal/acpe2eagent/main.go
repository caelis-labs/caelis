package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreconfig "github.com/OnslaughtSnail/caelis/core/config"
	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	storejsonl "github.com/OnslaughtSnail/caelis/internal/adapters/store/jsonl"
	toolshell "github.com/OnslaughtSnail/caelis/internal/adapters/tools/shell"
	toolspawn "github.com/OnslaughtSnail/caelis/internal/adapters/tools/spawn"
	tooltask "github.com/OnslaughtSnail/caelis/internal/adapters/tools/task"
	applocal "github.com/OnslaughtSnail/caelis/internal/app/local"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	coreacpserver "github.com/OnslaughtSnail/caelis/internal/surface/acpserver"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func main() {
	ctx := context.Background()
	provider, err := resolveProvider()
	if err != nil {
		log.Fatal(err)
	}
	store, err := storejsonl.New(sessionRootDir())
	if err != nil {
		log.Fatal(err)
	}
	settings, err := newSettings(ctx)
	if err != nil {
		log.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	stack, err := applocal.NewWithContext(ctx, applocal.Config{
		Runtime: coreconfig.Runtime{
			AppName:      "caelis",
			UserID:       "acp",
			WorkspaceKey: filepath.Base(cwd),
			WorkspaceCWD: cwd,
			Store:        coreconfig.Store{Backend: "memory"},
			Sandbox:      coreconfig.Sandbox{Backend: "host"},
		},
		Store:             store,
		Provider:          provider,
		Model:             scriptedModelProfile(),
		ExternalACPAgents: resolveExternalAgents(),
		BuiltinTools:      true,
		Settings:          settings,
		SystemPrompt:      strings.TrimSpace(os.Getenv("SDK_ACP_SYSTEM_PROMPT")),
	})
	if err != nil {
		log.Fatal(err)
	}
	services := stack.Services()
	if err := coreacpserver.ServeStdio(ctx, coreacpserver.Config{
		Engine:   stack.Engine(),
		Services: services,
		AppName:  services.AppName(),
		UserID:   services.UserID(),
		Implementation: schema.Implementation{
			Name:    "caelis-sdk",
			Title:   "Caelis SDK ACP Agent",
			Version: "0.1.0",
		},
	}, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func resolveProvider() (*scriptedProvider, error) {
	if mode := strings.TrimSpace(os.Getenv("SDK_ACP_SCRIPTED_MODE")); mode != "" {
		switch mode {
		case "async_command", "approval_command", "probe_spawn", "spawn", "spawn_passthrough", "mode_config":
			return &scriptedProvider{mode: mode}, nil
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
		return &scriptedProvider{text: reply, delay: delay}, nil
	}
	return nil, fmt.Errorf("set SDK_ACP_SCRIPTED_MODE or SDK_ACP_STUB_REPLY for acpe2eagent")
}

func newSettings(ctx context.Context) (*appsettings.Manager, error) {
	doc := appsettings.Document{
		Models: appsettings.ModelCatalog{
			DefaultID: "scripted",
			Configs:   []appsettings.ModelConfig{scriptedModelConfig()},
		},
		Skills: appsettings.SkillPolicy{
			LoadingMode:       appsettings.SkillLoadingModeExplicit,
			MaxExpansionChars: appsettings.DefaultSkillExpansionChars,
		},
	}
	return appsettings.NewManager(ctx, nil, doc)
}

func scriptedModelProfile() coreconfig.ModelProfile {
	return coreconfig.ModelProfile{
		ID:              "scripted",
		Alias:           "scripted",
		Provider:        "scripted",
		Model:           "scripted",
		ReasoningEffort: "medium",
	}
}

func scriptedModelConfig() appsettings.ModelConfig {
	return appsettings.ModelConfig{
		ID:                     "scripted",
		Alias:                  "scripted",
		Provider:               "scripted",
		Model:                  "scripted",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        4096,
		ReasoningMode:          "effort",
		ReasoningLevels:        []string{"low", "medium", "high"},
		ReasoningEffort:        "medium",
		DefaultReasoningEffort: "medium",
	}
}

func resolveExternalAgents() []acpexternal.Config {
	if strings.TrimSpace(os.Getenv("SDK_ACP_CHILD_NO_SPAWN")) == "1" {
		return nil
	}
	if strings.TrimSpace(os.Getenv("SDK_ACP_ENABLE_SPAWN")) != "1" {
		return nil
	}
	selfCmd := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_CMD"))
	if selfCmd == "" {
		return nil
	}
	name := strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_NAME"))
	if name == "" {
		name = "self"
	}
	return []acpexternal.Config{{
		AgentID:     name,
		AgentName:   name,
		Description: strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_DESC")),
		Command:     "bash",
		Args:        []string{"-lc", selfCmd},
		WorkDir:     strings.TrimSpace(os.Getenv("SDK_ACP_SELF_AGENT_WORKDIR")),
	}}
}

func sessionRootDir() string {
	if root := strings.TrimSpace(os.Getenv("SDK_ACP_SESSION_ROOT")); root != "" {
		return root
	}
	return filepath.Join(os.TempDir(), "caelis-sdk-acp-sessions")
}

type scriptedProvider struct {
	mode   string
	text   string
	delay  time.Duration
	mu     sync.Mutex
	calls  int
	taskID string
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]coremodel.ModelInfo, error) {
	return []coremodel.ModelInfo{{
		ID:                     "scripted",
		Name:                   "scripted",
		Provider:               "scripted",
		ContextWindowTokens:    128000,
		MaxOutputTokens:        4096,
		DefaultReasoningEffort: "medium",
		ReasoningEfforts:       []string{"low", "medium", "high"},
		SupportsToolCalls:      true,
		SupportsImages:         true,
		SupportsJSON:           true,
	}}, nil
}

func (p *scriptedProvider) Stream(ctx context.Context, req coremodel.Request) (coremodel.Stream, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	response, delay, err := p.response(req)
	if err != nil {
		return nil, err
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
	return &coremodel.StaticStream{Events: []coremodel.StreamEvent{{
		Type:     coremodel.StreamTurnDone,
		Response: &response,
	}}}, nil
}

func (p *scriptedProvider) response(req coremodel.Request) (coremodel.Response, time.Duration, error) {
	if strings.EqualFold(metaString(req.Meta, "caelis.purpose"), "approval_review") {
		return textResponse(`{"risk_level":"low","user_authorization":"explicit","outcome":"allow","rationale":"scripted approval"}`), 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode == "" {
		return textResponse(p.text), p.delay, nil
	}
	p.calls++
	switch p.mode {
	case "async_command":
		return p.asyncCommandResponse(req), 0, nil
	case "approval_command":
		return p.approvalCommandResponse(req), 0, nil
	case "probe_spawn":
		return p.probeSpawnResponse(req), 0, nil
	case "spawn":
		return p.spawnResponse(req), 0, nil
	case "spawn_passthrough":
		return p.spawnPassthroughResponse(req), 0, nil
	case "mode_config":
		return p.modeConfigResponse(req), 0, nil
	default:
		return coremodel.Response{}, 0, fmt.Errorf("unknown scripted mode %q", p.mode)
	}
}

func (p *scriptedProvider) asyncCommandResponse(req coremodel.Request) coremodel.Response {
	if p.calls == 2 {
		p.taskID = findTaskID(req)
	}
	switch p.calls {
	case 1:
		return toolResponse("command-async-1", toolshell.RunCommandToolName, map[string]any{
			"command":       "sleep 0.05; printf 'acpx async command ok'",
			"cwd":           ".",
			"yield_time_ms": 5,
		})
	case 2:
		return toolResponse("task-wait-1", tooltask.ToolName, map[string]any{
			"action":        "wait",
			"task_id":       p.taskID,
			"yield_time_ms": 300,
		})
	default:
		return textResponse("acpx async command ok")
	}
}

func (p *scriptedProvider) approvalCommandResponse(req coremodel.Request) coremodel.Response {
	if p.calls == 2 {
		p.taskID = findTaskID(req)
	}
	switch p.calls {
	case 1:
		return toolResponse("command-approval-1", toolshell.RunCommandToolName, map[string]any{
			"command":             "printf 'child approval ok\n'; sleep 0.2",
			"cwd":                 ".",
			"yield_time_ms":       5,
			"sandbox_permissions": "require_escalated",
			"justification":       "Do you want to run this command outside the sandbox?",
		})
	case 2:
		return toolResponse("task-wait-approval-1", tooltask.ToolName, map[string]any{
			"action":        "wait",
			"task_id":       p.taskID,
			"yield_time_ms": 300,
		})
	default:
		return textResponse("child approval ok")
	}
}

func (p *scriptedProvider) probeSpawnResponse(req coremodel.Request) coremodel.Response {
	for _, tool := range req.Tools {
		if strings.EqualFold(strings.TrimSpace(tool.Name), toolspawn.ToolName) {
			return textResponse("spawn enabled")
		}
	}
	return textResponse("spawn disabled")
}

func (p *scriptedProvider) spawnResponse(req coremodel.Request) coremodel.Response {
	if p.calls == 2 {
		p.taskID = findTaskID(req)
	}
	switch p.calls {
	case 1:
		return toolResponse("spawn-1", toolspawn.ToolName, map[string]any{
			"agent":  "self",
			"prompt": "Reply with exactly: spawn child ok",
		})
	case 2:
		return toolResponse("task-wait-spawn-1", tooltask.ToolName, map[string]any{
			"action":        "wait",
			"task_id":       p.taskID,
			"yield_time_ms": 300,
		})
	default:
		return textResponse("spawn child ok")
	}
}

func (p *scriptedProvider) spawnPassthroughResponse(req coremodel.Request) coremodel.Response {
	if p.calls == 2 {
		p.taskID = findTaskID(req)
	}
	resultText := firstNonEmpty(findTaskResult(req), "spawn child ok")
	switch p.calls {
	case 1:
		return toolResponse("spawn-pass-1", toolspawn.ToolName, map[string]any{
			"agent":  "self",
			"prompt": "Check whether SPAWN is available and reply with exactly the result.",
		})
	case 2:
		return toolResponse("task-wait-spawn-pass-1", tooltask.ToolName, map[string]any{
			"action":        "wait",
			"task_id":       p.taskID,
			"yield_time_ms": 300,
		})
	default:
		return textResponse(resultText)
	}
}

func (p *scriptedProvider) modeConfigResponse(req coremodel.Request) coremodel.Response {
	mode := firstNonEmpty(metaString(req.Meta, "caelis.session_mode"), "unknown")
	effort := firstNonEmpty(strings.TrimSpace(req.Reasoning.Effort), "none")
	return textResponse(fmt.Sprintf("mode=%s effort=%s", mode, effort))
}

func textResponse(text string) coremodel.Response {
	return coremodel.Response{
		Status: coremodel.ResponseCompleted,
		Message: coremodel.Message{
			Role:  coremodel.RoleAssistant,
			Parts: []coremodel.Part{coremodel.NewTextPart(strings.TrimSpace(text))},
		},
	}
}

func toolResponse(id string, name string, input map[string]any) coremodel.Response {
	return coremodel.Response{
		Status: coremodel.ResponseCompleted,
		Message: coremodel.Message{
			Role: coremodel.RoleAssistant,
			Parts: []coremodel.Part{{
				Kind: coremodel.PartToolUse,
				ToolUse: &coremodel.ToolCall{
					ID:    strings.TrimSpace(id),
					Name:  strings.TrimSpace(name),
					Input: mustJSON(input),
				},
			}},
		},
	}
}

func findTaskID(req coremodel.Request) string {
	for _, payload := range toolResultPayloads(req) {
		if taskID := firstNonEmpty(anyString(payload["task_id"]), anyString(payload["terminal_id"])); taskID != "" {
			return taskID
		}
	}
	return ""
}

func findTaskResult(req coremodel.Request) string {
	for _, payload := range toolResultPayloads(req) {
		if result := firstNonEmpty(
			anyString(payload["result"]),
			anyString(payload["final_message"]),
			anyString(payload["stdout"]),
		); result != "" {
			return result
		}
	}
	return ""
}

func toolResultPayloads(req coremodel.Request) []map[string]any {
	var payloads []map[string]any
	for _, message := range req.Messages {
		for _, part := range message.Parts {
			if part.Kind != coremodel.PartToolResult || part.ToolResult == nil {
				continue
			}
			for _, content := range part.ToolResult.Content {
				if content.Kind != coremodel.PartJSON || content.JSON == nil || len(content.JSON.Value) == 0 {
					continue
				}
				var payload map[string]any
				if err := json.Unmarshal(content.JSON.Value, &payload); err == nil && len(payload) > 0 {
					payloads = append(payloads, payload)
				}
			}
		}
	}
	return payloads
}

func metaString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	value, _ := meta[strings.TrimSpace(key)].(string)
	return strings.TrimSpace(value)
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func mustJSON(value map[string]any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func firstNonEmpty(values ...string) string {
	for _, one := range values {
		if strings.TrimSpace(one) != "" {
			return strings.TrimSpace(one)
		}
	}
	return ""
}
