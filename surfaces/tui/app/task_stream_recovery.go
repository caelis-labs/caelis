package tuiapp

import "time"

const taskStreamRecoveryBudget = 35 * time.Second

// Resolution, subscription and replacement failures share one recovery
// episode. A snapshot alone is not proof of a stable live follower.
func (m *Model) taskStreamRetryWithinBudget(callID string, now time.Time) bool {
	deadline := m.taskStreamRecoveryDeadline(callID, now)
	return now.Add(taskStreamRetryBackoff(taskStreamRetryBackoffCap)).Before(deadline)
}

func (m *Model) taskStreamRecoveryDeadline(callID string, now time.Time) time.Time {
	if m.taskStreamRecovery == nil {
		m.taskStreamRecovery = make(map[string]time.Time)
	}
	deadline := m.taskStreamRecovery[callID]
	if deadline.IsZero() {
		deadline = now.Add(taskStreamRecoveryBudget)
		m.taskStreamRecovery[callID] = deadline
	}
	return deadline
}

func (m *Model) noteTaskStreamFollowing(taskID string, now time.Time) {
	if m.taskStreamFollowing == nil {
		m.taskStreamFollowing = make(map[string]time.Time)
	}
	if m.taskStreamFollowing[taskID].IsZero() {
		m.taskStreamFollowing[taskID] = now
	}
	if now.Sub(m.taskStreamFollowing[taskID]) >= 5*time.Second {
		delete(m.taskStreamRecovery, m.taskStreamCallIDsByID[taskID])
		delete(m.taskStreamRetries, taskID)
	}
}
