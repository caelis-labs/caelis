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
	// observedTerminal forces the Task write that advances an activity cursor
	// even when Spawn already returned the same terminal generation.
	observedTerminal bool

	task          *subagentTask
	initialized   bool
	taskPersisted bool
	done          chan struct{}
	acknowledge   sync.Once
}

const subagentCompletionNoticeTimeout = 5 * time.Second

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
	runtime          *taskRuntime
	ctx              context.Context
	taskID           string
	turnSeq          int64
	observedTerminal bool
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

func newObservedSubagentCompletionSink(ctx context.Context, runtime *taskRuntime, taskID string, turnSeq int64) subagentCompletionSink {
	sink := newSubagentCompletionSink(ctx, runtime, taskID, turnSeq)
	sink.observedTerminal = true
	return sink
}

func (sink subagentCompletionSink) PublishSubagentCompletion(result delegation.Result) {
	done := sink.enqueue(result)
	if done != nil {
		<-done
	}
}

func (sink subagentCompletionSink) enqueue(result delegation.Result) <-chan struct{} {
	if sink.runtime == nil || sink.taskID == "" {
		return nil
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
		return nil
	}
	return sink.runtime.enqueueSubagentCompletion(&subagentCompletion{
		ctx:              sink.ctx,
		result:           result,
		turnSeq:          sink.turnSeq,
		observedTerminal: sink.observedTerminal,
		done:             make(chan struct{}),
	})
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
	completion, operationKey := tm.startSubagentCompletionLocked(taskID)
	tm.mu.Unlock()
	if completion != nil {
		go tm.applySubagentCompletion(completion, operationKey)
	}
}

// startSubagentCompletionLocked atomically transfers the Task operation slot
// to an already-enqueued producer completion. Callers must hold tm.mu.
func (tm *taskRuntime) startSubagentCompletionLocked(taskID string) (*subagentCompletion, string) {
	completion := tm.completions[taskID]
	if completion == nil {
		return nil, ""
	}
	if live := tm.subagents[taskID]; live != nil {
		completion.task = live
	}
	task := completion.task
	if task == nil {
		return nil, ""
	}
	if _, applying := tm.completionApplying[taskID]; applying {
		return nil, ""
	}
	operationKey := taskOperationKey(task.sessionRef, taskID)
	if _, active := tm.operations[operationKey]; active {
		return nil, ""
	}
	task.mu.Lock()
	ready := task.completionReady
	turnSeq := task.turnSeq
	task.mu.Unlock()
	if turnSeq > completion.turnSeq {
		delete(tm.completions, taskID)
		completion.acknowledgeDurable()
		return nil, ""
	}
	if !ready || turnSeq < completion.turnSeq {
		return nil, ""
	}
	tm.operations[operationKey] = struct{}{}
	tm.completionApplying[taskID] = struct{}{}
	return completion, operationKey
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
	current := tm.completions[taskID]
	if err != nil && current == completion {
		// Retain the Task operation claim across persistence retries. Releasing
		// it while the endpoint terminal waits for this acknowledgement would
		// let a new Task control operation enter the runner and form a lock
		// cycle with the pending completion.
		tm.mu.Unlock()
		time.AfterFunc(50*time.Millisecond, func() {
			tm.applySubagentCompletion(completion, operationKey)
		})
		return
	}
	delete(tm.operations, operationKey)
	delete(tm.completionApplying, taskID)
	if err == nil && current == completion {
		delete(tm.completions, taskID)
	}
	next, nextOperationKey := tm.startSubagentCompletionLocked(taskID)
	tm.mu.Unlock()

	if err == nil {
		completion.acknowledgeDurable()
		tm.publishSubagentCompletionNoticeAsync(completion)
	}
	if next != nil {
		go tm.applySubagentCompletion(next, nextOperationKey)
	}
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
		completion.taskPersisted = !task.running && !completion.observedTerminal
	}
	if !completion.taskPersisted {
		if task.running {
			task.seedStreamFromResult(completion.result)
			task.applyResult(completion.result)
		} else if completion.observedTerminal {
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
	if !snapshot.Running && snapshot.State != taskapi.StateCompleted {
		_ = tm.updateSubagentParticipant(completion.ctx, task, "updated")
	}
	return nil
}

// publishSubagentCompletionNoticeAsync keeps the optional parent hint outside
// the producer's durable Task/sidecar completion boundary. The idempotency key
// makes a later independent retry safe, but this bounded best-effort attempt
// never delays or reopens authoritative completion.
func (tm *taskRuntime) publishSubagentCompletionNoticeAsync(completion *subagentCompletion) {
	if tm == nil || tm.runtime == nil || completion == nil || completion.task == nil {
		return
	}
	ref, req, ok := subagentCompletionNotice(completion.task, completion.result, completion.turnSeq)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(completion.ctx), subagentCompletionNoticeTimeout)
	go func() {
		defer cancel()
		_, _ = tm.runtime.deliverAgentMessageToMain(ctx, ref, req)
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
	if result.State == delegation.StateCancelled || result.State == delegation.StateInterrupted {
		text = fmt.Sprintf("Subagent @%s is interrupted.", strings.TrimPrefix(handle, "@"))
	}
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
		rebased := tm.rebaseObservedSubagentTask(completion.task, entry)
		if rebased != nil {
			completion.taskPersisted = false
			tm.subagents[taskID] = completion.task
		}
	} else if turnSeq == completion.turnSeq && completion.observedTerminal && completion.task != nil {
		if tm.rebaseObservedSubagentTask(completion.task, entry) != nil {
			completion.taskPersisted = false
			tm.subagents[taskID] = completion.task
		}
	} else if turnSeq == completion.turnSeq {
		completion.task = durable
		completion.taskPersisted = !running && !completion.observedTerminal
		tm.subagents[taskID] = durable
	}
	tm.mu.Unlock()
}
