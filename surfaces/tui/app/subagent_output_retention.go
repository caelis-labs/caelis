package tuiapp

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

const childDisplayBytes = 4 << 20
const childDisplayEvents = 4096
const childDisplayViews = 8

// retainDisplayWindow releases objects, indexes and render caches, rather
// than merely hiding old rows. This projection is independent of child state.
func (v *subagentOutputView) retainDisplayWindow() {
	if v.document == nil {
		return
	}
	v.retentionUpdates++
	blocks := v.document.Blocks()
	large := false
	if v.block != nil && len(v.block.Events) > 0 {
		e := v.block.Events[len(v.block.Events)-1]
		large = len(e.Text)+len(e.Output)+len(e.Args)+len(e.FullArgs) > 128<<10
	}
	if v.retentionUpdates%64 != 0 && len(blocks) == v.retentionBlocks && !large {
		return
	}
	v.retentionBlocks = len(blocks)
	bytes, count := 0, 0
	changed := false
	for i := len(blocks) - 1; i >= 0; i-- {
		b, ok := blocks[i].(*ParticipantTurnBlock)
		if !ok {
			continue
		}
		kept := make([]SubagentEvent, 0, len(b.Events))
		blockChanged := false
		for j := len(b.Events) - 1; j >= 0; j-- {
			e := b.Events[j]
			if i < len(blocks)-2 && e.Kind != SEUserInput && e.Kind != SEAssistant && e.Kind != SEAgentCommunication {
				blockChanged = true
				continue
			}
			// A single streaming message/output also has a finite display tail.
			for _, value := range []*string{&e.Text, &e.Output, &e.Args, &e.StartArgs, &e.FullArgs, &e.OutputMessage, &e.TaskInput, &e.ApprovalText} {
				if len(*value) > 128<<10 {
					*value = childDisplayTail(*value)
					e.ActiveBuffer = nil
					blockChanged = true
				}
			}
			raw, _ := json.Marshal(e)
			size := len(raw) + 256
			if bytes+size > childDisplayBytes || count >= childDisplayEvents || i < len(blocks)-64 {
				blockChanged = true
				continue
			}
			bytes += size
			count++
			kept = append(kept, e)
		}
		if !blockChanged {
			continue
		}
		changed = true
		for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
			kept[l], kept[r] = kept[r], kept[l]
		}
		b.Events = kept
		b.toolEventIndex = nil
		b.toolPanelRenderCache = nil
		b.explorationProjection = explorationProjectionState{}
		// Indices changed, but retained message targets still own their live
		// deltas and final snapshots in the same narrative epoch.
		b.narrativeStream.targets = nil
		b.narrativeStream.pending = nil
		b.ExpandedTools, b.ExpandedToolOutput, b.ExpandedThought, b.ExpandedExplore, b.ExpandedAgentMessages = nil, nil, nil, nil, nil
		b.ToolPanelScroll = nil
		if len(kept) == 0 && b != v.block {
			v.document.Remove(b.BlockID())
			for key, block := range v.turnBlocks {
				if block == b {
					delete(v.turnBlocks, key)
				}
			}
			for key := range v.liveNarratives {
				if key.block == b {
					delete(v.liveNarratives, key)
				}
			}
		}
	}
	if len(v.seenProjections) > 2*childDisplayEvents {
		v.seenProjections = nil
	}
	if changed {
		retained := make(map[subagentOutputNarrativeKey]struct{})
		for _, raw := range v.document.Blocks() {
			b, ok := raw.(*ParticipantTurnBlock)
			if !ok {
				continue
			}
			for _, e := range b.Events {
				if !e.narrativeTracked || !strings.HasPrefix(e.narrativeTarget.identity, "message:") {
					continue
				}
				kind := TranscriptNarrativeAssistant
				if e.Kind == SEReasoning {
					kind = TranscriptNarrativeReasoning
				}
				key := subagentOutputNarrativeKey{block: b, kind: kind, messageID: strings.TrimPrefix(e.narrativeTarget.identity, "message:")}
				if _, exists := v.liveNarratives[key]; exists {
					retained[key] = struct{}{}
				}
			}
		}
		v.liveNarratives = retained
		document := NewDocument()
		for _, block := range v.document.Blocks() {
			document.Append(block)
		}
		v.document = document
		v.renderCache = subagentOutputRenderCache{}
		v.revision++
	}
}

func childDisplayTail(text string) string {
	start := len(text) - (64 << 10)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return "[Earlier display omitted]\n" + strings.Clone(text[start:])
}

// Keep a small number of cold panes. Eviction retains Task identity and drafts;
// reopening requests a fresh bounded snapshot using the normal history path.
func (m *Model) retainChildDisplayViews(selected string) {
	type candidate struct {
		id   string
		view *subagentOutputView
	}
	var views []candidate
	for id, v := range m.subagentOutputViews {
		if id != selected && v.document != nil && v.history == nil {
			views = append(views, candidate{id, v})
		}
	}
	sort.Slice(views, func(i, j int) bool { return views[i].view.lastViewed.After(views[j].view.lastViewed) })
	limit := childDisplayViews
	if v := m.subagentOutputViews[selected]; v != nil && v.document != nil {
		limit--
	}
	for _, c := range views[min(limit, len(views)):] {
		previous := c.view.block
		c.view.resetForReplacement()
		if previous != nil {
			c.view.block.Status, c.view.block.StartedAt, c.view.block.EndedAt = previous.Status, previous.StartedAt, previous.EndedAt
		}
		c.view.document = nil
		c.view.turnBlocks = nil
		c.view.historyBefore = ""
		if pane := c.view.pane; pane != nil {
			pane.composition = centeredOverlayCache{}
			pane.splitFrame = splitWorkspaceFrameCache{}
			pane.geometry = subagentOutputOverlayGeometry{}
		}
		delete(m.taskStreamCursors, m.taskStreamIDsByCallID[c.id])
	}
}
