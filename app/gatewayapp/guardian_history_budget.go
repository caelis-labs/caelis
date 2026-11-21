package gatewayapp

import (
	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	guardianHistoryMinWindow     = 64_000
	guardianReviewHighTurns      = 32
	guardianReviewKeepTurns      = 8
	guardianReviewTokens         = 32_000
	guardianUserTokens           = 32_000
	guardianAssistantEntryTokens = 900
	guardianAssistantHighEntries = 40
	guardianAssistantKeepEntries = 20
	guardianAssistantHighTokens  = 10_000
	guardianAssistantKeepTokens  = 5_000
	guardianSmallAssistantTokens = 2_000
)

func guardianSplitHistory(events []*session.Event) (users, assistants, reviews []*session.Event) {
	for _, event := range events {
		switch {
		case guardianIsUser(event):
			users = append(users, event)
		case guardianIsAssistantContext(event):
			assistants = append(assistants, event)
		case guardianTurn(event) != "":
			reviews = append(reviews, event)
		}
	}
	return users, assistants, reviews
}

func guardianMessageTokens(event *session.Event) int {
	message, ok := session.ModelMessageOf(event)
	if !ok {
		return 0
	}
	return sdkruntime.EvaluateModelRequestBudget(nil, &model.Request{Messages: []model.Message{message}}, sdkruntime.CompactionConfig{}).TotalTokens
}

func guardianHistoryUsage(events []*session.Event) (tokens, turns int) {
	previous := ""
	for _, event := range events {
		tokens += guardianMessageTokens(event)
		if turn := guardianTurn(event); turn != previous {
			turns++
			previous = turn
		}
	}
	return tokens, turns
}

func guardianRecentReviews(events []*session.Event, maxTurns, maxTokens int) []*session.Event {
	start := len(events)
	for maxTurns > 0 && start > 0 {
		previous := start
		key := guardianTurn(events[start-1])
		for previous > 0 && guardianTurn(events[previous-1]) == key {
			previous--
			maxTokens -= guardianMessageTokens(events[previous])
		}
		if maxTokens < 0 {
			break
		}
		start = previous
		maxTurns--
	}
	return events[start:]
}

func guardianRecentAssistants(events []*session.Event, maxEntries, maxTokens int) []*session.Event {
	start := len(events)
	for start > 0 && len(events)-start < maxEntries {
		maxTokens -= guardianMessageTokens(events[start-1])
		if maxTokens < 0 {
			break
		}
		start--
	}
	return events[start:]
}

func guardianRetainedHistory(events, users []*session.Event, optional ...[]*session.Event) []*session.Event {
	selectedUsers := make(map[string]*session.Event, len(users))
	for _, user := range users {
		selectedUsers[guardianUserKey(user)] = user
	}
	selected := make(map[*session.Event]bool)
	for _, group := range optional {
		for _, event := range group {
			selected[event] = true
		}
	}
	result := make([]*session.Event, 0, len(users)+len(selected))
	for _, event := range events {
		if guardianIsUser(event) {
			if user := selectedUsers[guardianUserKey(event)]; user != nil {
				result = append(result, user)
			}
		} else if selected[event] {
			result = append(result, event)
		}
	}
	return result
}

func guardianFitHistory(events []*session.Event, llm model.LLM, input string, output *model.OutputSpec, queryTools bool) ([]*session.Event, bool) {
	users, assistants, reviews := guardianSplitHistory(events)
	cfg := guardianCompactionConfig(llm, output, queryTools)
	fixed := sdkruntime.EvaluateModelRequestBudget(llm, guardianModelRequest(nil, input, output, queryTools), cfg)
	room := fixed.EffectiveInputBudget - fixed.TotalTokens
	if room < 0 {
		return users, false
	}
	userTokens, _ := guardianHistoryUsage(users)
	if fixed.ContextWindowTokens < guardianHistoryMinWindow {
		assistants = guardianRecentAssistants(assistants, guardianAssistantKeepEntries, min(guardianSmallAssistantTokens, room/4))
		assistantTokens, _ := guardianHistoryUsage(assistants)
		users = guardianTrimUserHistory(users, room-assistantTokens, guardianMessageTokens)
		userTokens, _ = guardianHistoryUsage(users)
		assistants = guardianRecentAssistants(assistants, guardianAssistantKeepEntries, room-userTokens)
		return guardianRetainedHistory(events, users, assistants), userTokens <= room
	}
	reviewTokens, reviewTurns := guardianHistoryUsage(reviews)
	assistantTokens, _ := guardianHistoryUsage(assistants)
	if reviewTurns >= guardianReviewHighTurns || reviewTokens > guardianReviewTokens || userTokens+assistantTokens+reviewTokens > room ||
		len(assistants) > guardianAssistantHighEntries || assistantTokens > guardianAssistantHighTokens {
		users = guardianTrimUserHistory(users, min(room, guardianUserTokens), guardianMessageTokens)
		userTokens, _ = guardianHistoryUsage(users)
		assistants = guardianRecentAssistants(assistants, guardianAssistantKeepEntries, min(room-userTokens, guardianAssistantKeepTokens))
		assistantTokens, _ = guardianHistoryUsage(assistants)
		reviews = guardianRecentReviews(reviews, guardianReviewKeepTurns, min(room-userTokens-assistantTokens, guardianReviewTokens))
		return guardianRetainedHistory(events, users, assistants, reviews), userTokens <= room
	}
	return events, true
}
