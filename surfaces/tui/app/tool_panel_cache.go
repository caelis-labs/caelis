package tuiapp

import (
	"hash/fnv"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
)

const maxGenericToolPanelCacheBytes = 64 * 1024

type toolOutputRenderCache struct {
	key            string
	rows           []RenderedRow
	bodyRenders    uint64
	lastInputBytes int
}

func renderCachedToolPanelRows(cache *map[string]toolOutputRenderCache, request toolPanelRenderRequest, scroll toolPanelScrollState) []RenderedRow {
	if cache == nil {
		return request.renderUncached()
	}
	if *cache == nil {
		*cache = map[string]toolOutputRenderCache{}
	}
	callID := strings.TrimSpace(request.CallID)
	if callID == "" {
		callID = strings.TrimSpace(request.ToolName)
	}
	renderRequest := request
	renderRequest.Text = toolPanelCacheText(request.ToolName, request.Text, request.Width)
	key := toolPanelRenderCacheKey(renderRequest, scroll)
	entry := (*cache)[callID]
	if entry.key == key && entry.rows != nil {
		return entry.rows
	}
	rows := renderRequest.renderUncached()
	entry.key = key
	entry.rows = rows
	entry.bodyRenders++
	entry.lastInputBytes = len(renderRequest.Text)
	(*cache)[callID] = entry
	return rows
}

func toolPanelRenderCacheKey(request toolPanelRenderRequest, scroll toolPanelScrollState) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(request.CallID))
	b.WriteByte(0)
	b.WriteString(strings.ToUpper(strings.TrimSpace(request.ToolName)))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(request.Width))
	b.WriteByte(0)
	b.WriteString(request.Ctx.renderThemeKey())
	b.WriteByte(0)
	b.WriteString(strconv.FormatBool(request.Err))
	b.WriteByte(0)
	b.WriteString(strings.TrimSpace(request.ClickToken))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(scroll.Offset))
	b.WriteByte(0)
	b.WriteString(strconv.FormatBool(scroll.FollowTail))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(len(request.Text)))
	b.WriteByte(0)
	b.WriteString(hashString64(request.Text))
	return b.String()
}

func toolPanelCacheText(toolName string, text string, width int) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if !displaypolicy.IsTerminalPanelTool(toolName, "") {
		return boundedGenericToolPanelText(text)
	}
	segments := tailWrappedTerminalSegmentsFromEnd(text, maxInt(1, width), acpTerminalPanelMaxLines)
	return strings.Join(segments, "\n")
}

func boundedGenericToolPanelText(text string) string {
	if len(text) <= maxGenericToolPanelCacheBytes {
		return text
	}
	const marker = "\n... output truncated for panel rendering ...\n"
	budget := maxGenericToolPanelCacheBytes - len(marker)
	if budget <= 0 {
		return prefixByBytes(text, maxGenericToolPanelCacheBytes)
	}
	head := budget / 2
	tail := budget - head
	return prefixByBytes(text, head) + marker + suffixByBytes(text, tail)
}

func prefixByBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func suffixByBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func toolOutputRenderKey(toolName string, output string, width int) string {
	text := toolPanelCacheText(toolName, output, width)
	return strconv.Itoa(len(text)) + ":" + hashString64(text)
}

func hashString64(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return strconv.FormatUint(h.Sum64(), 16)
}
