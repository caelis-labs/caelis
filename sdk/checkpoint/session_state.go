package checkpoint

import "maps"

const (
	StateKey = "checkpoint"
	MetaKey  = "checkpoint_meta"
)

func HasContent(state State) bool {
	state = NormalizeState(state)
	return state.Objective != "" ||
		len(state.UserConstraints) > 0 ||
		len(state.DurableDecisions) > 0 ||
		len(state.VerifiedFacts) > 0 ||
		len(state.CurrentProgress) > 0 ||
		len(state.OpenQuestionsAndRisks) > 0 ||
		len(state.NextActions) > 0 ||
		len(state.ActiveTasks) > 0 ||
		len(state.ActiveParticipants) > 0 ||
		len(state.LatestBlockers) > 0 ||
		len(state.OperationalAnnex.FilesTouched) > 0 ||
		len(state.OperationalAnnex.CommandsRun) > 0
}

func FromSessionState(state map[string]any) (State, Meta) {
	if len(state) == 0 {
		return State{}, Meta{}
	}
	return StateFromValue(state[StateKey]), MetaFromValue(state[MetaKey])
}

func PutSessionState(state map[string]any, checkpoint State, meta Meta) map[string]any {
	state = maps.Clone(state)
	if state == nil {
		state = map[string]any{}
	}
	checkpoint = NormalizeState(checkpoint)
	meta = NormalizeMeta(meta)
	if HasContent(checkpoint) {
		state[StateKey] = StateValue(checkpoint)
	} else {
		delete(state, StateKey)
	}
	if meta != (Meta{}) {
		state[MetaKey] = MetaValue(meta)
	} else {
		delete(state, MetaKey)
	}
	return state
}
