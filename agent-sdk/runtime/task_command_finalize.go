package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/commanddiag"
)

// finalizeTerminalCommand owns terminal Result reconciliation, diagnostics,
// durable completion, and live-registry removal. Running observations never
// enter this path.
func (tm *taskRuntime) finalizeTerminalCommand(
	ctx context.Context,
	task *commandTask,
	status sandbox.SessionStatus,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if status.Running {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: terminal command finalization requires a terminal status")
	}
	if err := tm.syncCommandOutput(ctx, task, status); err != nil {
		snapshot, persistErr := tm.markCommandUnknown(context.WithoutCancel(ctx), task, err)
		return snapshot, errors.Join(err, persistErr)
	}

	task.mu.Lock()
	outputText := task.output
	command := task.command
	state := stateFromStatus(status)
	task.state = state
	task.running = false
	task.metadata = map[string]any{
		"task_id":     task.ref.TaskID,
		"task_kind":   string(taskapi.KindCommand),
		"state":       string(state),
		"running":     false,
		"session_id":  task.ref.SessionID,
		"terminal_id": task.ref.TerminalID,
	}
	if status.Terminal.TerminalID != "" {
		task.metadata["terminal_id"] = status.Terminal.TerminalID
	}
	task.mu.Unlock()

	result, resultErr := task.session.Result(ctx)
	stdoutText := result.Stdout
	stderrText := result.Stderr
	finalText := terminalFinalText(outputText, stdoutText, stderrText, resultErr)
	finalOutputText := terminalOutputText(outputText, stdoutText, stderrText)
	diagnostic, hasDiagnostic := commanddiag.Best(commanddiag.Input{
		ToolName: shell.RunCommandToolName,
		Command:  command,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Error:    firstNonEmpty(strings.TrimSpace(result.Error), errorText(resultErr)),
		ExitCode: result.ExitCode,
		Route:    result.Route,
		Backend:  result.Backend,
	})

	task.mu.Lock()
	if !task.outputState.exact {
		task.reconcileFinalOutputLocked(finalOutputText)
	}
	exactStartCursor := task.outputState.frontier.base
	exactOutputDelta := ""
	finalOutputCursor := max(task.outputCursorLocked(), int64(len([]byte(finalOutputText))))
	if task.outputState.exact {
		exactOutputDelta = task.output
		finalOutputCursor = task.outputCursorLocked()
	}
	task.outputState.frontier.model = max(task.outputState.frontier.model, finalOutputCursor)
	task.commitOutputResumeCheckpointLocked()
	task.metadata["output_cursor"] = finalOutputCursor
	task.metadata["model_output_cursor"] = task.outputState.frontier.model
	task.metadata["output_checkpoint_available"] = task.outputState.checkpoint.available
	task.metadata["output_checkpoint_coherent"] = task.outputState.checkpoint.coherent
	task.metadata["output_recovery_gap"] = task.outputState.checkpoint.gap
	task.result = map[string]any{
		"state": string(state),
	}
	if taskOutputHasNonBlankLine(finalText) && strings.TrimSpace(finalText) != noOutputPlaceholder {
		task.result["result"] = finalText
	}
	if commandExitCodeAvailable(state, result.ExitCode, resultErr) {
		task.result["exit_code"] = result.ExitCode
	}
	if detail, ok := sandbox.SandboxPermissionDetail(result, resultErr); ok {
		task.result["error"] = detail
		task.result["error_code"] = string(tool.ErrorCodeSandboxDenied)
	} else if resultErr != nil && strings.TrimSpace(finalText) == noOutputPlaceholder && !sandbox.IsCommandExit(resultErr) {
		task.result["error"] = strings.TrimSpace(resultErr.Error())
		if code, _ := tool.ErrorPayload(resultErr)["error_code"].(string); code != "" {
			task.result["error_code"] = code
		}
	}
	if hasDiagnostic {
		if hint := strings.TrimSpace(diagnostic.Hint); hint != "" {
			task.result["system_hint"] = hint
		}
	}
	snapshot := commandObservationSnapshot(task.snapshotLocked(status), exactStartCursor, exactOutputDelta)
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return snapshot, err
	}
	tm.removeCommandTask(task)
	return snapshot, nil
}
