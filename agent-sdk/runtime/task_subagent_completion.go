package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

type subagentCompletion struct {
	ctx     context.Context
	result  delegation.Result
	turnSeq int64

	task          *subagentTask
	initialized   bool
	taskPersisted bool
	done          chan struct{}
	acknowledge   sync.Once
}

func (completion *subagentCompletion) acknowledgeDurable() {
	if completion == nil {
		return
	}
	completion.acknowledge.Do(func() {
		close(completion.done)
	})
}

// subagentCompletionSink binds producer authority to one Task turn. The
// producer cannot redirect completion by changing Result.TaskID.
type subagentCompletionSink struct {
	runtime *taskRuntime
	ctx     context.Context
	taskID  string
	turnSeq int64
}

func newSubagentCompletionSink(ctx context.Context, runtime *taskRuntime, taskID string, turnSeq int64) subagentCompletionSink {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = session.ContextWithControlMutation(
		context.WithoutCancel(ctx),
		session.ControlMutationPurposeSubagentCompletion,
	)
	return subagentCompletionSink{
		runtime: runtime,
		ctx:     ctx,
		taskID:  strings.TrimSpace(taskID),
		turnSeq: max(turnSeq, 1),
	}
}

func (sink subagentCompletionSink) PublishSubagentCompletion(result delegation.Result) {
	if sink.runtime == nil || sink.taskID == "" {
		return
	}
	result = delegation.CloneResult(result)
	result.TaskID = sink.taskID
	result.Running = false
	result.Yielded = false
	switch result.State {
	case delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome:
	default:
		return
	}
	done := sink.runtime.enqueueSubagentCompletion(&subagentCompletion{
		ctx:     sink.ctx,
		result:  result,
		turnSeq: sink.turnSeq,
		done:    make(chan struct{}),
	})
	if done != nil {
		<-done
	}
}

func (tm *taskRuntime) enqueueSubagentCompletion(completion *subagentCompletion) <-chan struct{} {
	if tm == nil || completion == nil {
		return nil
	}
	taskID := strings.TrimSpace(completion.result.TaskID)
	if taskID == "" {
		completion.acknowledgeDurable()
		return completion.done
	}
	tm.mu.Lock()
	current, exists := tm.completions[taskID]
	switch {
	case !exists || completion.turnSeq > current.turnSeq:
		if current != nil {
			current.acknowledgeDurable()
		}
		tm.completions[taskID] = completion
	case completion.turnSeq == current.turnSeq:
		completion.acknowledgeDurable()
		completion = current
	default:
		completion.acknowledgeDurable()
		tm.mu.Unlock()
		return completion.done
	}
	task := tm.subagents[taskID]
	ready := false
	discard := false
	if task != nil {
		completion.task = task
		task.mu.Lock()
		ready = task.completionReady
		discard = spawnPhaseOfTask(task) == spawnPhaseCompensated
		task.mu.Unlock()
	}
	if discard {
		delete(tm.completions, taskID)
		tm.mu.Unlock()
		completion.acknowledgeDurable()
		return completion.done
	}
	tm.mu.Unlock()
	if ready {
		tm.kickSubagentCompletion(taskID)
	}
	return completion.done
}

// discardSubagentCompletion releases a producer completion that raced a
// compensated Spawn. Once cancellation is durably recorded, the child no
// longer owns Task terminal state for that failed Spawn attempt.
func (tm *taskRuntime) discardSubagentCompletion(taskID string) {
	if tm == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	tm.mu.Lock()
	completion := tm.completions[taskID]
	delete(tm.completions, taskID)
	tm.mu.Unlock()
	if completion != nil {
		completion.acknowledgeDurable()
	}
}

// kickSubagentCompletion serializes a producer terminal with Task control
// without changing Task read/wait behavior. Spawn installation and active Task
// controls leave the completion queued; their release edges call this again.
func (tm *taskRuntime) kickSubagentCompletion(taskID string) {
	if tm == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	tm.mu.Lock()
	completion := tm.completions[taskID]
	if completion == nil {
		tm.mu.Unlock()
		return
	}
	if live := tm.subagents[taskID]; live != nil {
		completion.task = live
	}
	task := completion.task
	if task == nil {
		tm.mu.Unlock()
		return
	}
	if _, applying := tm.completionApplying[taskID]; applying {
		tm.mu.Unlock()
		return
	}
	operationKey := taskOperationKey(task.sessionRef, taskID)
	if _, active := tm.operations[operationKey]; active {
		tm.mu.Unlock()
		return
	}
	task.mu.Lock()
	ready := task.completionReady
	turnSeq := task.turnSeq
	task.mu.Unlock()
	if turnSeq > completion.turnSeq {
		delete(tm.completions, taskID)
		tm.mu.Unlock()
		completion.acknowledgeDurable()
		return
	}
	if !ready || turnSeq < completion.turnSeq {
		tm.mu.Unlock()
		return
	}
	tm.operations[operationKey] = struct{}{}
	tm.completionApplying[taskID] = struct{}{}
	tm.mu.Unlock()

	go tm.applySubagentCompletion(completion, operationKey)
}

func (tm *taskRuntime) applySubagentCompletion(completion *subagentCompletion, operationKey string) {
	err := tm.persistSubagentCompletion(completion)
	if err != nil {
		var conflict *taskapi.RevisionConflictError
		if errors.As(err, &conflict) {
			tm.refreshSubagentCompletionTask(completion)
		}
	}

	taskID := strings.TrimSpace(completion.result.TaskID)
	tm.mu.Lock()
	delete(tm.operations, operationKey)
	delete(tm.completionApplying, taskID)
	current := tm.completions[taskID]
	if err == nil && current == completion {
		delete(tm.completions, taskID)
	}
	pending := tm.completions[taskID]
	tm.mu.Unlock()

	if err == nil {
		completion.acknowledgeDurable()
		tm.publishSubagentCompletionNoticeAsync(completion)
		if pending != nil {
			tm.kickSubagentCompletion(taskID)
		}
		return
	}
	if current != completion {
		return
	}
	time.AfterFunc(50*time.Millisecond, func() {
		tm.kickSubagentCompletion(taskID)
	})
}

// persistSubagentCompletion owns the producer completion transaction. It keeps
// Task persistence separate from the sidecar final/checkpoint so a failure in
// either phase retries exactly the unfinished phase without reopening Task wait
// as a lifecycle trigger.
func (tm *taskRuntime) persistSubagentCompletion(completion *subagentCompletion) error {
	if tm == nil || completion == nil || completion.task == nil {
		return errors.New("agent-sdk/runtime: subagent completion task is unavailable")
	}
	task := completion.task
	task.mu.Lock()
	if task.turnSeq != completion.turnSeq {
		task.mu.Unlock()
		return nil
	}
	if !completion.initialized {
		completion.initialized = true
		// A terminal Task observed before this completion already crossed its
		// durable mutation boundary under the serialized Task operation claim.
		completion.taskPersisted = !task.running
	}
	if !completion.taskPersisted {
		if task.running {
			task.seedStreamFromResult(completion.result)
			task.applyResult(completion.result)
		}
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(completion.ctx, entry); err != nil {
			return err
		}
		completion.taskPersisted = true
	} else {
		task.mu.Unlock()
	}

	if err := tm.appendSideSubagentFinalEvent(completion.ctx, task); err != nil {
		return err
	}
	snapshot := task.snapshot()
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		if tm.subagents[task.ref.TaskID] == task {
			delete(tm.subagents, task.ref.TaskID)
		}
		tm.mu.Unlock()
		_ = tm.updateSubagentParticipant(completion.ctx, task, "updated")
	}
	return nil
}

// publishSubagentCompletionNoticeAsync keeps the optional parent hint outside
// the producer's durable Task/sidecar completion boundary. Its independent
// goroutine may wait behind an earlier Session write, preserving FIFO without
// delaying or reopening authoritative completion.
func (tm *taskRuntime) publishSubagentCompletionNoticeAsync(completion *subagentCompletion) {
	if tm == nil || tm.runtime == nil || completion == nil || completion.task == nil {
		return
	}
	ref, req, ok := subagentCompletionNotice(completion.task, completion.result, completion.turnSeq)
	if !ok {
		return
	}
	go func() {
		_, _ = tm.runtime.deliverAgentMessageToMain(context.WithoutCancel(completion.ctx), ref, req)
	}()
}

func subagentCompletionNotice(task *subagentTask, result delegation.Result, turnSeq int64) (session.SessionRef, agentmessage.Request, bool) {
	if task == nil {
		return session.SessionRef{}, agentmessage.Request{}, false
	}
	task.mu.Lock()
	ref := session.NormalizeSessionRef(task.sessionRef)
	handle := strings.TrimSpace(task.handle)
	if handle == "" {
		task.mu.Unlock()
		return session.SessionRef{}, agentmessage.Request{}, false
	}
	role := subagentParticipantRole(task)
	agentID := strings.TrimSpace(task.anchor.AgentID)
	taskID := strings.TrimSpace(task.ref.TaskID)
	task.mu.Unlock()

	state := strings.TrimSpace(string(result.State))
	text := fmt.Sprintf("Subagent @%s is %s. Use Task read with handle %s for its full result.", strings.TrimPrefix(handle, "@"), state, handle)
	return ref, agentmessage.Request{
		MessageID: fmt.Sprintf("subagent-completion:%s:%d", taskID, max(turnSeq, 1)),
		To:        agentmessage.Parent, Text: text,
		From: session.ActorRef{
			Kind: session.ActorKindParticipant, ID: agentID,
			Name: "@" + strings.TrimPrefix(handle, "@"), Role: string(role),
		},
		Scope: &session.EventScope{Source: "subagent_completion", Participant: session.ParticipantRef{
			ID: agentID, Kind: session.ParticipantKindSubagent,
			Role: role, DelegationID: taskID,
		}},
	}, true
}

// refreshSubagentCompletionTask reloads only after a CAS conflict. Ordinary
// persistence and sidecar-final failures retain the in-memory terminal result,
// which may be intentionally absent from the canonical Task index.
func (tm *taskRuntime) refreshSubagentCompletionTask(completion *subagentCompletion) {
	if tm == nil || tm.store == nil || completion == nil {
		return
	}
	taskID := strings.TrimSpace(completion.result.TaskID)
	entry, err := tm.store.Get(context.WithoutCancel(completion.ctx), taskID)
	if err != nil || entry == nil || entry.Kind != taskapi.KindSubagent {
		return
	}
	durable := tm.rehydrateSubagentTask(entry)
	if durable == nil {
		return
	}
	durable.mu.Lock()
	turnSeq := durable.turnSeq
	running := durable.running
	durable.mu.Unlock()

	tm.mu.Lock()
	if tm.completions[taskID] != completion {
		tm.mu.Unlock()
		return
	}
	if turnSeq > completion.turnSeq {
		delete(tm.completions, taskID)
		tm.mu.Unlock()
		completion.acknowledgeDurable()
		return
	}
	if turnSeq < completion.turnSeq && completion.task != nil {
		rebased := tm.rebaseAcceptedSubagentTask(completion.task, entry)
		if rebased != nil {
			completion.taskPersisted = false
			tm.subagents[taskID] = completion.task
		}
	} else if turnSeq == completion.turnSeq {
		completion.task = durable
		completion.taskPersisted = !running
		tm.subagents[taskID] = durable
	}
	tm.mu.Unlock()
}
