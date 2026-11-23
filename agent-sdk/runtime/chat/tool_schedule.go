package chat

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type stepToolCallResult struct {
	index   int
	message model.Message
	event   *session.Event
	err     error
}

func (a *Agent) executeStepToolCalls(
	ctx context.Context,
	stepID string,
	calls []model.ToolCall,
	yieldProgress func(*session.Event) bool,
	visibility *toolVisibility,
) ([]model.Message, []*session.Event, bool, error) {
	if len(calls) == 0 {
		return nil, nil, true, nil
	}
	if len(calls) == 1 {
		toolMessage, toolEvent, err := a.executeToolCallWithProgressAdmitted(ctx, calls[0], modelStepRef(stepID, 0, len(calls)), yieldProgress, visibility)
		if toolEvent == nil {
			return nil, nil, true, err
		}
		return []model.Message{toolMessage}, []*session.Event{toolEvent}, true, err
	}
	schedules, ordered := a.nativeToolSchedules(ctx, calls)
	if ordered {
		return a.executeStepToolCallsOrdered(ctx, stepID, calls, schedules, yieldProgress, visibility)
	}
	return a.executeStepToolCallsConcurrent(ctx, calls, concurrentStepRefs(stepID, len(calls), 0, len(calls)), yieldProgress, visibility)
}

func (a *Agent) executeStepToolCallsConcurrent(
	ctx context.Context,
	calls []model.ToolCall,
	stepRefs []*tool.ModelStepRef,
	yieldProgress func(*session.Event) bool,
	visibility *toolVisibility,
) ([]model.Message, []*session.Event, bool, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	progressCh := make(chan *session.Event, len(calls)*16)
	doneCh := make(chan stepToolCallResult, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		i, call := i, call
		wg.Add(1)
		go func() {
			defer wg.Done()
			toolMessage, toolEvent, err := a.executeToolCallWithProgressAdmitted(callCtx, call, stepRefs[i], func(event *session.Event) bool {
				if event == nil {
					return true
				}
				select {
				case progressCh <- event:
					return true
				case <-callCtx.Done():
					return false
				}
			}, visibility)
			doneCh <- stepToolCallResult{index: i, message: toolMessage, event: toolEvent, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(doneCh)
		close(progressCh)
	}()

	results := make([]stepToolCallResult, len(calls))
	remaining := len(calls)
	var firstErr error
	// Nil the parent-done channel after the first cancel so select cannot
	// busy-spin on a permanently ready ctx.Done while draining completions.
	parentDone := ctx.Done()
	for remaining > 0 {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			if progress != nil && yieldProgress != nil && !yieldProgress(progress) {
				cancel()
				wg.Wait()
				return nil, nil, false, nil
			}
		case result, ok := <-doneCh:
			if !ok {
				doneCh = nil
				continue
			}
			results[result.index] = result
			remaining--
			if result.err != nil && firstErr == nil {
				firstErr = result.err
				cancel()
			}
		case <-parentDone:
			// Drain in-flight completions so terminal tool journals are not dropped.
			cancel()
			parentDone = nil
		}
	}
	if progressCh != nil {
		for progress := range progressCh {
			if progress != nil && yieldProgress != nil && !yieldProgress(progress) {
				cancel()
				return nil, nil, false, nil
			}
		}
	}
	messages := make([]model.Message, 0, len(results))
	events := make([]*session.Event, 0, len(results))
	for _, result := range results {
		messages = append(messages, result.message)
		events = append(events, result.event)
	}
	return messages, events, true, firstErr
}

func (a *Agent) executeStepToolCallsOrdered(
	ctx context.Context,
	stepID string,
	calls []model.ToolCall,
	schedules []toolbinding.NativeSchedule,
	yieldProgress func(*session.Event) bool,
	visibility *toolVisibility,
) ([]model.Message, []*session.Event, bool, error) {
	messages := make([]model.Message, 0, len(calls))
	events := make([]*session.Event, 0, len(calls))
	for index := 0; index < len(calls); {
		var groupMessages []model.Message
		var groupEvents []*session.Event
		var err error
		if pairsScriptWaitWithCancel(schedules, index) {
			// Drain both controls and publish their results before considering
			// another call, even when wait collects a terminal receipt.
			end := index + 2
			var ok bool
			groupMessages, groupEvents, ok, err = a.executeStepToolCallsConcurrent(ctx, calls[index:end], concurrentStepRefs(stepID, len(calls), index, end), yieldProgress, visibility)
			if !ok {
				return nil, nil, false, nil
			}
			index = end
		} else {
			message, event, callErr := a.executeToolCallWithProgressAdmitted(ctx, calls[index], modelStepRef(stepID, index, len(calls)), yieldProgress, visibility)
			groupMessages, groupEvents, err = []model.Message{message}, []*session.Event{event}, callErr
			index++
		}

		for i, event := range groupEvents {
			if event == nil {
				continue
			}
			messages = append(messages, groupMessages[i])
			events = append(events, event)
			if err == nil && yieldProgress != nil {
				if !yieldProgress(event) {
					return nil, nil, false, nil
				}
				if a.yieldedResults == nil {
					a.yieldedResults = map[*session.Event]bool{}
				}
				a.yieldedResults[event] = true
			}
		}
		if err != nil {
			return messages, events, true, err
		}
	}
	return messages, events, true, nil
}

func modelStepRef(stepID string, index int, callCount int) *tool.ModelStepRef {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" || index < 0 || callCount <= 0 || index >= callCount {
		return nil
	}
	return &tool.ModelStepRef{ID: stepID, Index: index, CallCount: callCount}
}

func concurrentStepRefs(stepID string, callCount, start, end int) []*tool.ModelStepRef {
	refs := tool.NewConcurrentModelStepRefs(stepID, callCount)
	if refs == nil {
		return make([]*tool.ModelStepRef, end-start)
	}
	// Preserve the original model positions, but only this group participates
	// in the approval barrier. Other calls may never be dispatched.
	for i, ref := range refs {
		if i < start || i >= end {
			ref.MarkAdmissionComplete()
		}
	}
	return refs[start:end]
}

func (a *Agent) nativeToolSchedules(ctx context.Context, calls []model.ToolCall) ([]toolbinding.NativeSchedule, bool) {
	if caps, _ := model.CapabilitiesOf(a.model); !caps.ParallelToolCalls {
		return nil, true
	}
	schedules := make([]toolbinding.NativeSchedule, len(calls))
	ordered := false
	for i, call := range calls {
		prepared := a.preparedCalls[call.ID]
		if prepared.target == nil {
			ordered = true
			continue
		}
		schedule := toolbinding.ScheduleNativeCall(ctx, prepared.target, prepared.call)
		ordered = ordered || schedule.Ordered
		if prepared.err == nil && prepared.result == nil {
			schedules[i] = schedule
		}
	}
	return schedules, ordered
}

func pairsScriptWaitWithCancel(schedules []toolbinding.NativeSchedule, index int) bool {
	// Never move a cancellation across another effect or a terminal barrier.
	return index+1 < len(schedules) && schedules[index].ScriptWait != "" &&
		schedules[index].ScriptWait == schedules[index+1].ScriptCancel
}
