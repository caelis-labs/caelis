package runtime

import (
	"context"
	"errors"
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
		return resolvedStreamTask{}, errorcode.New(errorcode.Unavailable, "Task stream service is unavailable")
	}
	if taskID == "" {
		return resolvedStreamTask{}, errorcode.New(errorcode.InvalidArgument, "Task identity is required")
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
			fmt.Sprintf("load Task stream metadata for %q", taskID),
			err,
		)
	}
	if entry == nil || strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return resolvedStreamTask{}, streamTaskNotFound(taskID)
	}
	if strings.TrimSpace(entry.TaskID) != taskID {
		return resolvedStreamTask{}, errorcode.New(
			errorcode.FailedPrecondition,
			fmt.Sprintf("durable Task identity for %q does not match", taskID),
		)
	}
	if entry.Kind != taskapi.KindCommand && entry.Kind != taskapi.KindSubagent {
		return resolvedStreamTask{}, errorcode.New(
			errorcode.FailedPrecondition,
			fmt.Sprintf("Task %q has unsupported stream kind %q", taskID, entry.Kind),
		)
	}

	backfilled, err := tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		var sessionFence *session.FenceConflictError
		if errors.As(err, &sessionFence) {
			// Stream resolution is observation-only. If the producing Runtime
			// owns the Session fence, abandon this observer's repair write and
			// use the current durable Task without retrying unfenced.
			reloaded, loadErr := tm.store.Get(context.WithoutCancel(ctx), taskID)
			if loadErr == nil &&
				storedTaskEntryMatches(reloaded, ref, entry.Kind) &&
				strings.TrimSpace(reloaded.TaskID) == taskID {
				entry = reloaded
				err = nil
			} else {
				err = errorcode.Wrap(
					errorcode.Unavailable,
					fmt.Sprintf("reload fence-contended Task stream metadata for %q", taskID),
					errors.Join(err, loadErr),
				)
			}
		}
	} else {
		entry = backfilled
	}
	if err != nil {
		return resolvedStreamTask{}, wrapStreamTaskResolutionError(
			errorcode.Unavailable,
			fmt.Sprintf("recover canonical Task stream metadata for %q", taskID),
			err,
		)
	}
	switch entry.Kind {
	case taskapi.KindCommand:
		command, err := tm.rehydrateCommandTask(entry)
		if err != nil {
			return resolvedStreamTask{}, wrapStreamTaskResolutionError(
				errorcode.FailedPrecondition,
				fmt.Sprintf("rehydrate command Task stream %q", taskID),
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
				fmt.Sprintf("rehydrate subagent Task stream %q", taskID),
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
	return resolvedStreamTask{}, errorcode.New(errorcode.Internal, "resolved Task stream kind was not handled")
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
			fmt.Sprintf("Task %q has ambiguous live stream ownership", taskID),
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
	return errorcode.New(errorcode.NotFound, fmt.Sprintf("task %q not found", strings.TrimSpace(taskID)))
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
