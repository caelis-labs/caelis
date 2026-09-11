package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/google/uuid"
)

// LoadHistory establishes a loaded, idle child connection. ACP owns the
// history; its ordered replay completion is sent to the observation writer.
// Later authorized input reuses this connection. Opening history alone never
// submits a prompt, applies execution configuration, or closes the Session.
func (r *Runner) LoadHistory(ctx context.Context, raw tasksubagent.HistoryRequest) (session.LoadedSession, error) {
	if r == nil || ctx == nil {
		return session.LoadedSession{}, fmt.Errorf("subagent history context is required")
	}
	req := tasksubagent.CloneHistoryRequest(raw)
	target := childEndpointFromReconnect(req.Anchor, &req.Reconnect)
	if err := r.BindChildEndpoint(ctx, target, req.Reconnect.Spawn); err != nil {
		return session.LoadedSession{}, err
	}
	slot, err := r.lookupChildSlot(target)
	if err != nil {
		return session.LoadedSession{}, err
	}
	slot.opMu.Lock()
	defer slot.opMu.Unlock()
	run := slot.currentRun()
	if run != nil {
		run.mu.RLock()
		active, state := run.running || run.finishing, run.state
		run.mu.RUnlock()
		if active || state == delegation.StateUnknownOutcome {
			return session.LoadedSession{}, errorcode.New(errorcode.Unavailable, "Cannot reload child history while its producer is active or unsettled")
		}
		if err := run.client.Close(ctx); err != nil {
			return session.LoadedSession{}, err
		}
	}
	_, loaded, err := r.loadChildEndpointLocked(ctx, req.Anchor, &req.Reconnect, slot, false)
	return loaded, err
}

func childRecoveryEnvironment(cfg AgentConfig) map[string]string {
	launchEnv := maps.Clone(cfg.Env)
	if strings.EqualFold(strings.TrimSpace(cfg.Name), "self") {
		if launchEnv == nil {
			launchEnv = map[string]string{}
		}

		launchEnv["SDK_ACP_ENABLE_SPAWN"] = "0"
		launchEnv["SDK_ACP_CHILD_NO_SPAWN"] = "1"
	}
	return launchEnv
}

type historyCollector struct {
	mu sync.Mutex

	runner            *Runner
	run               childRun
	turnSeq           int64
	lastUpdateType    string
	lastUserMessageID string
	inputStart        int
	userText          *session.Event
	userTextBody      strings.Builder
	events            []*session.Event
	err               error
	bytes             int
}

func newHistoryCollector(runner *Runner, anchor delegation.Anchor, agentName string) *historyCollector {
	return &historyCollector{
		runner: runner,
		run: childRun{
			anchor: anchor, taskID: strings.TrimSpace(anchor.TaskID), agentName: strings.TrimSpace(agentName),
		},
	}
}

func (c *historyCollector) observe(env client.UpdateEnvelope) {
	if c == nil || c.runner == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	raw := env.Raw
	if len(raw) == 0 {
		raw, c.err = json.Marshal(env.Update)
		if c.err != nil {
			return
		}
	}
	c.bytes += len(raw)
	if len(c.events) >= 8192 || c.bytes > 32<<20 {
		c.err = errorcode.New(errorcode.ResourceExhausted, "Child replay exceeds observation budget")
		return
	}
	wantSessionID := strings.TrimSpace(c.run.anchor.SessionID)
	gotSessionID := strings.TrimSpace(env.SessionID)
	if gotSessionID != wantSessionID {
		if c.err == nil {
			c.err = fmt.Errorf(
				"session/load update belongs to Session %q, want %q",
				gotSessionID,
				wantSessionID,
			)
		}
		return
	}
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
	updateType := historyUpdateType(env.Update)
	newInputTurn := false
	if updateType == client.UpdateUserMessage {
		messageID := ""
		if chunk, ok := env.Update.(client.ContentChunk); ok {
			messageID = strings.TrimSpace(chunk.MessageID)
		}
		if c.lastUpdateType != client.UpdateUserMessage || messageID != "" && messageID != c.lastUserMessageID {
			c.flushUserText()
			c.turnSeq++
			newInputTurn = true
		}
		c.lastUserMessageID = messageID
	}
	if c.turnSeq <= 0 {
		c.turnSeq = 1
	}
	if updateType != client.UpdateUserMessage {
		c.flushUserText()
	}
	event := c.run.acpUpdateEvent(env, c.runner.clock())
	if event == nil {
		c.lastUpdateType = updateType
		return
	}
	if updateType == client.UpdateUserMessage {
		if newInputTurn {
			// Unmarked ACP input is a user message. Agent mail supplies its
			// source in a header/footer; history does not identify the product
			// principal, so never invent an authenticated user ID here.
			c.run.inputActor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
			c.inputStart = len(c.events)
		}
		media := event.Message != nil && acputil.ContentPartsContainImage(model.ContentPartsFromParts(event.Message.Parts))
		if media {
			c.flushUserText()
		}
		quotedUser := false
		if !media && (c.userText != nil || event.Text != "" &&
			(strings.HasPrefix(userPromptOpen, event.Text) || strings.HasPrefix(event.Text, userPromptOpen))) {
			if c.userText == nil {
				c.userText = event
			}
			previousBytes := c.userTextBody.Len()
			c.userTextBody.WriteString(event.Text)
			text := c.userTextBody.String()
			// Inspect only new bytes plus the possible split delimiter. Tiny
			// ACP chunks must not repeatedly copy or rescan the whole input.
			closed := strings.Contains(text[max(0, previousBytes-len(userPromptClose)+1):], userPromptClose)
			if strings.HasPrefix(userPromptOpen, text) || strings.HasPrefix(text, userPromptOpen) && !closed {
				c.lastUpdateType = updateType
				return
			}
			event, c.userText = c.userText, nil
			event.Text = text
			c.userTextBody.Reset()
			if body, ok := unquoteUserPrompt(text); ok {
				event.Text = body
				quotedUser = true
				c.run.inputActor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
				c.inputStart = len(c.events) + 1
			}
			// Only a complete envelope excludes legacy parsing. Agent mail can
			// quote an envelope in its body, followed by its own sender footer.
		}
		if !quotedUser {
			event.Text = stripLoadedCollaborationSetup(event.Text)
		}
		if strings.TrimSpace(event.Text) == "" && !media {
			c.lastUpdateType = updateType
			return
		}
		if source, body, format := loadedAgentCommunicationPrompt(event.Text); !quotedUser && format != loadedMailNone {
			c.run.inputActor = source
			event.Text = body
			if format == loadedMailFooter {
				// ACP can replay a mail body/media and its footer as separate blocks.
				// Legacy headers apply only to following content.
				for _, previous := range c.events[c.inputStart:] {
					markSubagentInputEvent(previous, source)
				}
			}
			c.inputStart = len(c.events) + 1
			if strings.TrimSpace(body) == "" {
				c.inputStart = len(c.events)
				c.lastUpdateType = updateType
				return
			}
		}
		if !media {
			message := model.NewTextMessage(model.RoleUser, event.Text)
			event.Message = &message
			if event.Protocol != nil && event.Protocol.Update != nil {
				event.Protocol.Update.Content = session.ProtocolTextContent(event.Text)
			}
		}
		c.run.inputActor = markSubagentInputEvent(event, c.run.inputActor)
	}
	c.appendHistoryEvent(event)
	c.lastUpdateType = updateType
}

func (c *historyCollector) appendHistoryEvent(event *session.Event) {
	if event.Scope == nil {
		event.Scope = &session.EventScope{}
	}
	event.Scope.TurnID = fmt.Sprintf("%s:%d", strings.TrimSpace(c.run.taskID), c.turnSeq)
	event.SessionID = strings.TrimSpace(c.run.anchor.SessionID)
	event.ID = fmt.Sprintf("subagent-load:%s:%d", strings.TrimSpace(c.run.taskID), len(c.events)+1)
	c.events = append(c.events, event)
}

// A truncated or merely similar prefix is literal input, never discarded.
func (c *historyCollector) flushUserText() {
	if c.userText == nil {
		return
	}
	event := c.userText
	c.userText = nil
	event.Text = c.userTextBody.String()
	c.userTextBody.Reset()
	message := model.NewTextMessage(model.RoleUser, event.Text)
	event.Message = &message
	if event.Protocol != nil && event.Protocol.Update != nil {
		event.Protocol.Update.Content = session.ProtocolTextContent(event.Text)
	}
	markSubagentInputEvent(event, session.ActorRef{Kind: session.ActorKindUser, Name: "user"})
	c.appendHistoryEvent(event)
}

type loadedMailFormat uint8

const (
	loadedMailNone loadedMailFormat = iota
	loadedMailHeader
	loadedMailFooter
)

// loadedAgentCommunicationPrompt reconstructs display-only sender provenance
// from Caelis mail footers and retained legacy headers. The result is
// used only for child transcript replay; it never authorizes routing or input.
func loadedAgentCommunicationPrompt(text string) (session.ActorRef, string, loadedMailFormat) {
	if split := strings.LastIndex(text, "\n\nFrom: "); split >= 0 {
		body, name := text[:split], strings.TrimSpace(text[split+len("\n\nFrom: "):])
		if name == "" || strings.ContainsAny(name, "\r\n") {
			return session.ActorRef{}, "", loadedMailNone
		}
		actor := session.ActorRef{Kind: session.ActorKindSystem, Name: name}
		if name == session.AgentCommunicationParentHandle {
			actor = session.ParentCommunicationActor()
		}
		// Mail references are model-visible; the body alone is display text.
		if start := strings.LastIndex(body, "\n\nMessage-ID: "); start >= 0 {
			lines := strings.Split(body[start+2:], "\n")
			_, idErr := uuid.Parse(strings.TrimPrefix(lines[0], "Message-ID: "))
			valid := idErr == nil && len(lines) <= 2
			if len(lines) == 2 {
				_, replyErr := uuid.Parse(strings.TrimPrefix(lines[1], "In-Reply-To: "))
				valid = valid && strings.HasPrefix(lines[1], "In-Reply-To: ") && replyErr == nil
			}
			if valid {
				body = body[:start]
			}
		}
		return actor, body, loadedMailFooter
	}
	// Legacy headers remain readable for retained external session history.
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "[Internal agent message]" {
		return session.ActorRef{}, "", loadedMailNone
	}
	actor := session.ActorRef{}
	messageLine := -1
	for index := 1; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		switch {
		case line == "Message:":
			messageLine = index
		case strings.HasPrefix(line, "Sender: "):
			actor.Name = strings.TrimSpace(strings.TrimPrefix(line, "Sender: "))
		case strings.HasPrefix(line, "Kind: "):
			actor.Kind = session.ActorKind(strings.TrimSpace(strings.TrimPrefix(line, "Kind: ")))
		case strings.HasPrefix(line, "Role: "):
			actor.Role = strings.TrimSpace(strings.TrimPrefix(line, "Role: "))
		case strings.HasPrefix(line, "Sender ID: "):
			actor.ID = strings.TrimSpace(strings.TrimPrefix(line, "Sender ID: "))
		default:
			return session.ActorRef{}, "", loadedMailNone
		}
		if messageLine >= 0 {
			break
		}
	}
	if messageLine < 0 {
		return session.ActorRef{}, "", loadedMailNone
	}
	if actor.Kind == "" {
		// Compatibility for Agent communication prompts written before Kind was
		// included in the header. The identity remains display-only.
		actor.Kind = session.ActorKindSystem
	}
	if actor.Kind == session.ActorKindController {
		actor = session.ParentCommunicationActor()
	}
	if err := session.ValidateAgentCommunicationActor(actor); err != nil {
		return session.ActorRef{}, "", loadedMailNone
	}
	return session.CloneActorRef(actor), strings.TrimSpace(strings.Join(lines[messageLine+1:], "\n")), loadedMailHeader
}

// Setup is visible to the external model but omitted from loaded child display.
func stripLoadedCollaborationSetup(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasSuffix(trimmed, collaboration.SliceCloseTag) {
		return text
	}
	if start := strings.LastIndex(trimmed, collaboration.SliceOpenTag+"\n"); start >= 0 &&
		(start == 0 || strings.HasSuffix(trimmed[:start], "\n")) {
		return strings.TrimSpace(trimmed[:start])
	}
	return text
}

func historyUpdateType(update client.Update) string {
	switch typed := update.(type) {
	case client.ContentChunk:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.ToolCall:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.ToolCallUpdate:
		return strings.TrimSpace(typed.SessionUpdate)
	case client.PlanUpdate:
		return strings.TrimSpace(typed.SessionUpdate)
	default:
		return ""
	}
}

func (c *historyCollector) eventsSnapshot() []*session.Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushUserText()
	out := make([]*session.Event, 0, len(c.events))
	for _, event := range c.events {
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func (c *historyCollector) errSnapshot() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

var _ tasksubagent.HistoryRunner = (*Runner)(nil)
