package tuiapp

import (
	"context"
	"fmt"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestEarlierHistoryPrependKeepsLiveBlocksAndPhysicalViewport(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {35, 16}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := newViewportDemandTestModel(t, 4)
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.setViewportFollowState(viewportPinnedHistory)
			m.viewport.SetYOffset(0)
			m.materializeVisibleViewport()
			anchor := m.viewportAnchorAt(m.viewportVisibleOffset())
			old := m.doc
			tail := m.mainTimelineTailID
			before := m.View().Content
			builder := NewModel(Config{NoAnimation: true, NoColor: true})
			builder.doc.Append(&demandTestBlock{id: "earlier", lines: 40})
			cancel := func() {}
			build := &earlierHistoryBuild{generation: m.viewGeneration, before: "older-token", model: builder, cancel: cancel, next: "oldest-token"}
			m.earlierHistory = map[string]*earlierHistoryBuild{"": build}
			m.sessionHistoryBefore = build.before
			// Output may arrive while the older request is outstanding.
			live := &demandTestBlock{id: "live-during-history", lines: 3}
			m.doc.Append(live)
			m.handleEarlierHistory(earlierHistoryMsg{build: build, done: true})
			if m.doc == old || m.doc.Find(live.id) != live || m.mainTimelineTailID != tail || m.sessionHistoryBefore != "oldest-token" {
				t.Fatal("prepend replaced live state")
			}
			got := m.viewportAnchorAt(m.viewportVisibleOffset())
			if got.blockID != anchor.blockID || got.row != anchor.row {
				t.Fatalf("anchor moved: %v -> %v", anchor, got)
			}
			frames := []string{before, m.View().Content}
			terminal := vt.NewSafeEmulator(size[0], size[1])
			defer terminal.Close()
			for n, output := range renderFullscreenFramesForTest(t, size[0], size[1], frames...) {
				if _, err := terminal.Write([]byte(output)); err != nil {
					t.Fatal(err)
				}
				if got, want := trimPhysicalFramePadding(ansi.Strip(terminal.Render())), trimPhysicalFramePadding(ansi.Strip(frames[n])); got != want {
					t.Fatalf("physical prepend frame differs:\n%s\nwant:\n%s", got, want)
				}
			}
		})
	}
}

func TestEarlierHistoryFailureAndGenerationDoNotReplaceDocument(t *testing.T) {
	m, _ := newViewportDemandTestModel(t, 2)
	old := m.doc
	build := &earlierHistoryBuild{generation: m.viewGeneration, before: "token", cancel: func() {}, model: NewModel(Config{})}
	m.earlierHistory = map[string]*earlierHistoryBuild{"": build}
	m.sessionHistoryBefore = "token"
	m.handleEarlierHistory(earlierHistoryMsg{build: build, done: true, err: context.Canceled})
	if m.doc != old || m.sessionHistoryBefore != "token" || len(m.earlierHistory) != 0 {
		t.Fatal("failure advanced history")
	}
	m.earlierHistory[""] = build
	m.viewGeneration++
	m.handleEarlierHistory(earlierHistoryMsg{build: build, done: true})
	if m.doc != old {
		t.Fatal("stale request changed new Session")
	}
}

func TestEarlierChildPrependKeepsLiveTurnAndSelection(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.ensureSubagentOutputView("spawn")
	view.taskHandle = "child"
	for n := range 3 {
		view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: fmt.Sprint(n), NarrativeKind: TranscriptNarrativeAssistant, Text: strings.Repeat("live row\n", 10), Final: true})
	}
	view.pane = &subagentOutputOverlayState{offset: 3, selectStart: textSelectionPoint{line: 3, col: 0}, selectEnd: textSelectionPoint{line: 4, col: 3}}
	rows := m.subagentOutputRows(view, 60, 20)
	anchor := rows[3]
	current := view.block
	turn := view.turnID
	// This live append is deliberately not rendered before the older page commits.
	view.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: turn, NarrativeKind: TranscriptNarrativeAssistant, Text: "late output\n", Final: true})
	older := *view
	older.resetForReplacement()
	older.observeChildEvent(TranscriptEvent{Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent, TurnID: "older", NarrativeKind: TranscriptNarrativeAssistant, Text: "older response", Final: true})
	m.prependChildHistory(view, &older)
	if view.block != current || view.turnID != turn {
		t.Fatal("older page rewound live Turn")
	}
	if got := view.renderCache.rows[view.pane.offset]; got.BlockID != anchor.BlockID || got.Plain != anchor.Plain {
		t.Fatal("child visible row moved")
	}
	if view.pane.selectStart.line != view.pane.offset || view.pane.selectEnd.line != view.pane.offset+1 {
		t.Fatal("child selection moved")
	}
}

type earlierSessionLoaderStub struct {
	interruptBridgeStub
	requests chan string
	release  chan struct{}
	feed     *observationTestFeed
}

func (s *earlierSessionLoaderStub) LoadSessionHistory(ctx context.Context, id, before string) (appserver.FeedSubscription, error) {
	s.requests <- id + ":" + before
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return s.feed, nil
	}
}

func TestEarlierHistoryPageUpUsesIndependentRequestAndCommitsOnce(t *testing.T) {
	feed := newObservationTestFeed("session")
	<-feed.feed
	feed.publish(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session", Scope: eventstream.ScopeMain, TurnID: "older", Final: true, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, Content: eventstream.TextContent{Type: "text", Text: "older final response"}}})
	feed.feed <- appserver.FeedDelivery{Kind: appserver.FeedDeliverySync, Source: appserver.FeedSourceExact}
	service := &earlierSessionLoaderStub{feed: feed, requests: make(chan string, 4), release: make(chan struct{})}
	messages := make(chan tea.Msg, 16)
	sender := &ProgramSender{Send: func(msg tea.Msg) { messages <- msg }}
	defer sender.Close()
	m := NewModel(Config{NoColor: true, NoAnimation: true, ControlService: service, ProgramSender: sender})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.currentSessionID = "session"
	m.handleUserMessageMsg(UserMessageMsg{Text: "current"})
	m.sessionHistoryBefore = "token"
	old := m.doc
	m.Update(subagentSpecialKey(tea.KeyPgUp))
	select {
	case got := <-service.requests:
		if got != "session:token" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("PageUp did not demand older history")
	}
	m.Update(subagentSpecialKey(tea.KeyPgUp))
	if len(service.requests) != 0 || m.doc != old {
		t.Fatal("duplicate request or early commit")
	}
	close(service.release)
	done := false
	for !done {
		select {
		case msg := <-messages:
			if page, ok := msg.(earlierHistoryMsg); ok {
				if !page.done && m.doc != old {
					t.Fatal("partial page became visible")
				}
				done = page.done
			}
			m.Update(msg)
		case <-time.After(time.Second):
			t.Fatal("history request did not complete")
		}
	}

	text := ""
	for _, row := range m.doc.RenderAll(m.blockRenderContext(80)) {
		text += row.Plain + "\n"
	}
	if m.doc == old || m.sessionHistoryBefore != "" || !strings.Contains(text, "older final response") {
		t.Fatal("older window did not commit at origin")
	}

}

func TestEarlierChildDemandUsesResolvedTaskID(t *testing.T) {
	service := &subagentRosterTestTaskStreamService{subscribeRequests: make(chan taskstream.SubscribeRequest, 4)}
	sender := &ProgramSender{Send: func(tea.Msg) {}}
	defer sender.Close()
	m := NewModel(Config{NoColor: true, NoAnimation: true, TaskStreams: bindTaskStreamTestClient(t, service), ProgramSender: sender})
	m.currentSessionID = "session"
	view := m.ensureSubagentOutputView("spawn")
	view.taskHandle = "human-handle"
	view.historyBefore = "older-token"
	m.taskStreamIDsByCallID["spawn"] = "resolved-task-id"
	m.taskStreamCursors["resolved-task-id"] = "live-cursor"
	m.subagentOutputOverlay = &subagentOutputOverlayState{callID: "spawn"}
	m.scrollSubagentOutputOverlay(-1)
	select {
	case req := <-service.subscribeRequests:
		if req.TaskID != "resolved-task-id" || req.HistoryBefore != "older-token" || req.Cursor != "" || req.Follow {
			t.Fatalf("wrong history request: %+v", req)
		}
	case <-time.After(time.Second):
		t.Fatal("child scroll did not demand history")
	}
	if m.taskStreamCursors["resolved-task-id"] != "live-cursor" {
		t.Fatal("history request changed live cursor")
	}
}
