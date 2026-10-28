package providers

import (
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
)

// Preserve both the previous assistant's input boundary and the frozen request
// boundary when several user messages (including a compact instruction) follow
// it. Breakpoints belong to the encoded request, never canonical history.
func applyAnthropicCacheBreakpoints(params *anthropic.MessageNewParams) {
	var users []int
	lastAssistant := -1
	for i, message := range params.Messages {
		switch message.Role {
		case anthropic.MessageParamRoleUser:
			users = append(users, i)
		case anthropic.MessageParamRoleAssistant:
			lastAssistant = i
		}
	}
	selected := map[*anthropic.CacheControlEphemeralParam]bool{}
	mark := func(point *anthropic.CacheControlEphemeralParam) {
		if point == nil || selected[point] || len(selected) == 4 {
			return
		}
		*point = anthropic.NewCacheControlEphemeralParam()
		point.TTL = anthropic.CacheControlEphemeralTTLTTL5m
		selected[point] = true
	}
	markUser := func(index int) {
		blocks := params.Messages[index].Content
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].OfText != nil && strings.TrimSpace(blocks[i].OfText.Text) == "" {
				continue
			}
			if point := blocks[i].GetCacheControl(); point != nil {
				mark(point)
				return
			}
		}
	}
	if len(users) > 0 {
		markUser(users[len(users)-1])
	}
	for i := len(users) - 1; i >= 0; i-- {
		if users[i] < lastAssistant {
			markUser(users[i])
			break
		}
	}
	if len(users) > 1 {
		markUser(users[len(users)-2])
	}
	for i := len(params.System) - 1; i >= 0; i-- {
		if strings.TrimSpace(params.System[i].Text) != "" {
			mark(&params.System[i].CacheControl)
			break
		}
	}
	if len(params.Tools) > 0 {
		mark(params.Tools[len(params.Tools)-1].GetCacheControl())
	}
}
