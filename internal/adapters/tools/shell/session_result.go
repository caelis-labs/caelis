package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

func SessionResult(ctx context.Context, call tool.Call, name string, action string, session sandbox.Session, cursor sandbox.OutputCursor, wait time.Duration) (tool.Result, error) {
	if session == nil {
		return tool.Result{}, errors.New("tools/shell: task session is required")
	}
	if wait > 0 {
		waitCtx, cancel := context.WithTimeout(ctx, wait)
		_, err := session.Wait(waitCtx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return tool.Result{}, err
		}
	}
	snapshot, err := session.Snapshot(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	output, err := session.Read(ctx, cursor)
	if err != nil {
		return tool.Result{}, err
	}
	payload := sessionPayload(action, snapshot, output)
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    name,
		IsError: snapshot.State == sandbox.SessionFailed,
		Content: []model.Part{
			model.NewTextPart(sessionSummary(payload)),
			{
				Kind: model.PartJSON,
				JSON: &model.JSONPart{Value: raw},
			},
		},
		Meta: sessionMeta(action, snapshot, output, session),
	}, nil
}

func sessionPayload(action string, snapshot sandbox.SessionSnapshot, output sandbox.OutputSnapshot) map[string]any {
	state := normalizedSessionState(snapshot)
	payload := map[string]any{
		"action":         strings.TrimSpace(action),
		"state":          string(state),
		"running":        snapshot.Running,
		"task_id":        snapshot.Ref.ID,
		"backend":        string(snapshot.Ref.Backend),
		"terminal_id":    snapshot.Terminal.ID,
		"supports_input": snapshot.SupportsInput,
		"stdout_cursor":  output.Cursor.Stdout,
		"stderr_cursor":  output.Cursor.Stderr,
	}
	if command := strings.TrimSpace(snapshot.Command); command != "" {
		payload["command"] = command
	}
	if cwd := strings.TrimSpace(snapshot.Dir); cwd != "" {
		payload["cwd"] = cwd
	}
	if !snapshot.StartedAt.IsZero() {
		payload["started_at"] = snapshot.StartedAt
	}
	if !snapshot.UpdatedAt.IsZero() {
		payload["updated_at"] = snapshot.UpdatedAt
	}
	if output.Stdout != "" {
		payload["stdout"] = output.Stdout
	}
	if output.Stderr != "" {
		payload["stderr"] = output.Stderr
	}
	if output.StdoutDroppedBytes > 0 {
		payload["stdout_dropped_bytes"] = output.StdoutDroppedBytes
	}
	if output.StderrDroppedBytes > 0 {
		payload["stderr_dropped_bytes"] = output.StderrDroppedBytes
	}
	for key, value := range snapshot.Metadata {
		key = strings.TrimSpace(key)
		if key != "" {
			payload[key] = value
		}
	}
	if !snapshot.Running {
		payload["exit_code"] = snapshot.ExitCode
	}
	if errText := strings.TrimSpace(snapshot.Error); errText != "" {
		payload["error"] = errText
	}
	return payload
}

func sessionMeta(action string, snapshot sandbox.SessionSnapshot, output sandbox.OutputSnapshot, session sandbox.Session) map[string]any {
	task := map[string]any{
		"action":        strings.TrimSpace(action),
		"state":         string(normalizedSessionState(snapshot)),
		"running":       snapshot.Running,
		"task_id":       snapshot.Ref.ID,
		"terminal_id":   snapshot.Terminal.ID,
		"stdout_cursor": output.Cursor.Stdout,
		"stderr_cursor": output.Cursor.Stderr,
	}
	if backend := strings.TrimSpace(string(snapshot.Ref.Backend)); backend != "" {
		task["backend"] = backend
	}
	if command := strings.TrimSpace(snapshot.Command); command != "" {
		task["command"] = command
	}
	if cwd := strings.TrimSpace(snapshot.Dir); cwd != "" {
		task["cwd"] = cwd
	}
	task["supports_input"] = snapshot.SupportsInput
	if !snapshot.StartedAt.IsZero() {
		task["started_at"] = snapshot.StartedAt
	}
	if !snapshot.UpdatedAt.IsZero() {
		task["updated_at"] = snapshot.UpdatedAt
	}
	if !snapshot.Running {
		task["exit_code"] = snapshot.ExitCode
	}
	if errText := strings.TrimSpace(snapshot.Error); errText != "" {
		task["error"] = errText
	}
	addRuntimeTaskPreview(task, output, "output_text", false)
	if snapshot.OutputPreview != nil {
		addRuntimeTaskPreview(task, *snapshot.OutputPreview, "", true)
	}
	for key, value := range snapshot.Metadata {
		key = strings.TrimSpace(key)
		if key != "" {
			task[key] = value
		}
	}
	if provider, ok := session.(interface{ TaskMeta() map[string]any }); ok {
		for key, value := range provider.TaskMeta() {
			key = strings.TrimSpace(key)
			if key != "" {
				task[key] = value
			}
		}
	}
	return tool.WithRuntimeTaskMeta(map[string]any{
		"task_id":       snapshot.Ref.ID,
		"state":         string(normalizedSessionState(snapshot)),
		"running":       snapshot.Running,
		"stdout_cursor": output.Cursor.Stdout,
		"stderr_cursor": output.Cursor.Stderr,
	}, task)
}

func normalizedSessionState(snapshot sandbox.SessionSnapshot) sandbox.SessionState {
	if snapshot.State != "" {
		return snapshot.State
	}
	if snapshot.Running {
		return sandbox.SessionRunning
	}
	if snapshot.ExitCode == 0 {
		return sandbox.SessionCompleted
	}
	return sandbox.SessionFailed
}

func addRuntimeTaskPreview(task map[string]any, output sandbox.OutputSnapshot, textKey string, replace bool) {
	preview := tool.RuntimeTaskPreview(output.Stdout, output.Stderr, output.StdoutDroppedBytes, output.StderrDroppedBytes, output.Cursor.Stdout, output.Cursor.Stderr)
	for key, value := range preview {
		if replace {
			task[key] = value
			continue
		}
		if _, exists := task[key]; !exists {
			task[key] = value
		}
	}
	if textKey != "" {
		if text := tool.JoinRuntimeTaskStreams(output.Stdout, output.Stderr); text != "" {
			task[textKey] = text
		}
	}
}

func sessionSummary(payload map[string]any) string {
	var parts []string
	if taskID, _ := payload["task_id"].(string); taskID != "" {
		parts = append(parts, "task_id: "+taskID)
	}
	if state, _ := payload["state"].(string); state != "" {
		parts = append(parts, "state: "+state)
	}
	if stdout, _ := payload["stdout"].(string); strings.TrimSpace(stdout) != "" {
		parts = append(parts, "stdout:\n"+strings.TrimRight(stdout, "\n"))
	}
	if stderr, _ := payload["stderr"].(string); strings.TrimSpace(stderr) != "" {
		parts = append(parts, "stderr:\n"+strings.TrimRight(stderr, "\n"))
	}
	if exitCode, ok := payload["exit_code"].(int); ok {
		parts = append(parts, fmt.Sprintf("exit_code: %d", exitCode))
	}
	if errText, _ := payload["error"].(string); strings.TrimSpace(errText) != "" {
		parts = append(parts, "error: "+strings.TrimSpace(errText))
	}
	if len(parts) == 0 {
		return "state: unknown"
	}
	return strings.Join(parts, "\n\n")
}
