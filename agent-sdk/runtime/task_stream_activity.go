package runtime

import (
	"context"
	"strings"
	"sync"
)

type taskStreamActivitySignal struct {
	generation uint64
	watchers   int
	changed    chan struct{}
}

// taskStreamActivityWatch reserves one generation from before reader
// resolution until Follow begins waiting. The reservation closes the otherwise
// lossy window between those operations and lets the signal map discard entries
// as soon as no observer can still depend on them.
type taskStreamActivityWatch struct {
	tasks      *taskRuntime
	key        string
	signal     *taskStreamActivitySignal
	generation uint64
	release    sync.Once
}

func (tm *taskRuntime) watchTaskStreamActivity(sessionID, taskID string) *taskStreamActivityWatch {
	if tm == nil {
		return nil
	}
	key := taskStreamActivityKey(sessionID, taskID)
	if key == "" {
		return nil
	}
	tm.mu.Lock()
	signal := tm.taskStreamActivitySignalLocked(key)
	signal.watchers++
	watch := &taskStreamActivityWatch{tasks: tm, key: key, signal: signal, generation: signal.generation}
	tm.mu.Unlock()
	return watch
}

func (watch *taskStreamActivityWatch) Await(ctx context.Context) error {
	if watch == nil || watch.tasks == nil || watch.signal == nil {
		return context.Canceled
	}
	defer watch.Close()
	watch.tasks.mu.Lock()
	if watch.signal.generation != watch.generation {
		watch.tasks.mu.Unlock()
		return nil
	}
	changed := watch.signal.changed
	watch.tasks.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	}
}

func (watch *taskStreamActivityWatch) Close() {
	if watch == nil || watch.tasks == nil || watch.signal == nil {
		return
	}
	watch.release.Do(func() {
		watch.tasks.mu.Lock()
		watch.signal.watchers--
		if watch.signal.watchers <= 0 && watch.tasks.streamActivity[watch.key] == watch.signal {
			delete(watch.tasks.streamActivity, watch.key)
		}
		watch.tasks.mu.Unlock()
	})
}

func (tm *taskRuntime) notifyTaskStreamActivity(sessionID, taskID string) {
	if tm == nil {
		return
	}
	key := taskStreamActivityKey(sessionID, taskID)
	if key == "" {
		return
	}
	tm.mu.Lock()
	signal := tm.taskStreamActivitySignalLocked(key)
	signal.generation++
	close(signal.changed)
	if signal.watchers == 0 {
		delete(tm.streamActivity, key)
	} else {
		signal.changed = make(chan struct{})
	}
	tm.mu.Unlock()
}

func (tm *taskRuntime) taskStreamActivitySignalLocked(key string) *taskStreamActivitySignal {
	if tm.streamActivity == nil {
		tm.streamActivity = map[string]*taskStreamActivitySignal{}
	}
	signal := tm.streamActivity[key]
	if signal == nil {
		signal = &taskStreamActivitySignal{changed: make(chan struct{})}
		tm.streamActivity[key] = signal
	}
	return signal
}

func taskStreamActivityKey(sessionID, taskID string) string {
	sessionID = strings.TrimSpace(sessionID)
	taskID = strings.TrimSpace(taskID)
	if sessionID == "" || taskID == "" {
		return ""
	}
	return sessionID + "\x00" + taskID
}
