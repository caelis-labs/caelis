package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

type resolvedStreamTask struct {
	kind     taskapi.Kind
	command  *commandTask
	subagent *subagentTask
}

// resolveStreamTask pins one stream reader to an already-live task or recovers
// it through the durable entry's declared kind. Recovery never probes the
// command and subagent registries in sequence, so one lookup failure cannot be
// replaced by an unrelated fallback error.
func (tm *taskRuntime) resolveStreamTask(ctx context.Context, ref session.SessionRef, taskID string) (resolvedStreamTask, error) {
	ref = session.NormalizeSessionRef(ref)
	taskID = strings.TrimSpace(taskID)
	if tm == nil {
		return resolvedStreamTask{}, errorcode.New(errorcode.Unavailable, "agent-sdk/runtime: task stream service is unavailable")
	}
	if taskID == "" {
		return resolvedStreamTask{}, errorcode.New(errorcode.InvalidArgument, "agent-sdk/runtime: task id is required")
	}
	if resolved, found, err := tm.resolveLiveStreamTask(ref, taskID); found || err != nil {
		return resolved, err
	}
	if tm.store == nil {
		return resolvedStreamTask{}, streamTaskNotFound(taskID)
	}

	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return resolvedStreamTask{}, wrapStreamTaskResolutionError(
			errorcode.Unavailable,
			fmt.Sprintf("agent-sdk/runtime: load task stream metadata for %q", taskID),
			err,
		)
	}
	if entry == nil || strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return resolvedStreamTask{}, streamTaskNotFound(taskID)
	}
	if strings.TrimSpace(entry.TaskID) != taskID {
		return resolvedStreamTask{}, errorcode.New(
			errorcode.FailedPrecondition,
			fmt.Sprintf("agent-sdk/runtime: durable task identity for %q does not match", taskID),
		)
	}
	if entry.Kind != taskapi.KindCommand && entry.Kind != taskapi.KindSubagent {
		return resolvedStreamTask{}, errorcode.New(
			errorcode.FailedPrecondition,
			fmt.Sprintf("agent-sdk/runtime: task %q has unsupported stream kind %q", taskID, entry.Kind),
		)
	}

	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return resolvedStreamTask{}, wrapStreamTaskResolutionError(
			errorcode.Unavailable,
			fmt.Sprintf("agent-sdk/runtime: recover canonical task stream metadata for %q", taskID),
			err,
		)
	}
	switch entry.Kind {
	case taskapi.KindCommand:
		command, err := tm.rehydrateCommandTask(entry)
		if err != nil {
			return resolvedStreamTask{}, wrapStreamTaskResolutionError(
				errorcode.FailedPrecondition,
				fmt.Sprintf("agent-sdk/runtime: rehydrate command task stream %q", taskID),
				err,
			)
		}
		tm.installCommandTask(command)
		return resolvedStreamTask{kind: taskapi.KindCommand, command: command}, nil
	case taskapi.KindSubagent:
		subagent := tm.rehydrateSubagentTask(entry)
		if subagent == nil {
			return resolvedStreamTask{}, errorcode.New(
				errorcode.FailedPrecondition,
				fmt.Sprintf("agent-sdk/runtime: rehydrate subagent task stream %q", taskID),
			)
		}
		tm.mu.Lock()
		if current := tm.subagents[taskID]; current != nil &&
			strings.TrimSpace(current.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID) {
			tm.mu.Unlock()
			return resolvedStreamTask{kind: taskapi.KindSubagent, subagent: current}, nil
		}
		tm.subagents[taskID] = subagent
		tm.rememberTaskHandleLocked(subagent.sessionRef.SessionID, subagent.handle)
		tm.mu.Unlock()
		return resolvedStreamTask{kind: taskapi.KindSubagent, subagent: subagent}, nil
	}
	return resolvedStreamTask{}, errorcode.New(errorcode.Internal, "agent-sdk/runtime: resolved task stream kind was not handled")
}

func (tm *taskRuntime) resolveLiveStreamTask(ref session.SessionRef, taskID string) (resolvedStreamTask, bool, error) {
	tm.mu.RLock()
	command := tm.tasks[taskID]
	subagent := tm.subagents[taskID]
	tm.mu.RUnlock()

	commandMatches := command != nil &&
		strings.TrimSpace(command.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID)
	subagentMatches := subagent != nil &&
		strings.TrimSpace(subagent.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID)
	switch {
	case commandMatches && subagentMatches:
		return resolvedStreamTask{}, true, errorcode.New(
			errorcode.FailedPrecondition,
			fmt.Sprintf("agent-sdk/runtime: task %q has ambiguous live stream ownership", taskID),
		)
	case commandMatches:
		return resolvedStreamTask{kind: taskapi.KindCommand, command: command}, true, nil
	case subagentMatches:
		return resolvedStreamTask{kind: taskapi.KindSubagent, subagent: subagent}, true, nil
	case command != nil || subagent != nil:
		return resolvedStreamTask{}, true, streamTaskNotFound(taskID)
	default:
		return resolvedStreamTask{}, false, nil
	}
}

func streamTaskNotFound(taskID string) error {
	return errorcode.New(errorcode.NotFound, fmt.Sprintf("agent-sdk/runtime: task %q not found", strings.TrimSpace(taskID)))
}

func wrapStreamTaskResolutionError(fallback errorcode.Code, message string, err error) error {
	if err == nil {
		return errorcode.New(fallback, message)
	}
	code := errorcode.CodeOf(err)
	if code == errorcode.Unknown {
		code = fallback
	}
	return errorcode.Wrap(code, message, err)
}
