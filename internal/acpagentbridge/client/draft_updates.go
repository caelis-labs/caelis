package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

type draftMessage struct {
	content    []json.RawMessage
	hasContent bool
	meta       map[string]any
	cacheBytes int
}

type draftTerminal struct {
	toolID     string
	info       map[string]any
	exit       map[string]any
	cacheBytes int
	// Only unassociated output is retained until its first tool reference.
	// Once projected, the canonical terminal owner retains emitted bytes.
	output        []byte
	outputPresent bool
	exitPresent   bool
}

func (d *draftClient) handleUpdate(raw json.RawMessage) {
	var notification v2.UpdateSessionNotification
	if err := json.Unmarshal(raw, &notification); err != nil {
		d.fail(err)
		return
	}
	if err := notification.Validate(); err != nil {
		d.fail(err)
		return
	}
	// Preserve raw patch presence. The alpha.8 typed structs intentionally do
	// not retain all absent/null/empty distinctions on re-encoding.
	var envelope SessionNotification
	if err := json.Unmarshal(raw, &envelope); err != nil {
		d.fail(err)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Update, &fields); err != nil {
		d.fail(err)
		return
	}
	var kind string
	_ = json.Unmarshal(fields["sessionUpdate"], &kind)
	d.mu.Lock()
	_, allocationErr := d.ensureSessionLocked(envelope.SessionID)
	d.mu.Unlock()
	if allocationErr != nil {
		d.fail(allocationErr)
		return
	}
	if kind == "state_update" {
		d.mu.Lock()
		d.sessionLocked(envelope.SessionID).narrativeKey = ""
		d.mu.Unlock()
		if err := d.observeState(envelope.SessionID, fields); err != nil {
			d.fail(err)
		}
		return
	}
	updates, err := d.decodeUpdates(envelope.SessionID, kind, fields, envelope.Update)
	if err != nil {
		d.fail(err)
		return
	}
	if d.client.cfg.OnUpdate == nil {
		return
	}
	for _, update := range updates {
		d.client.cfg.OnUpdate(UpdateEnvelope{SessionID: envelope.SessionID, Update: NormalizeInboundUpdate(update), Raw: append(json.RawMessage(nil), envelope.Update...)})
	}
}

func (d *draftClient) decodeUpdates(sessionID, kind string, fields map[string]json.RawMessage, raw json.RawMessage) ([]Update, error) {
	switch kind {
	case "user_message", "agent_message", "agent_thought", UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
	default:
		d.mu.Lock()
		d.sessionLocked(sessionID).narrativeKey = ""
		d.mu.Unlock()
	}
	switch kind {
	case "user_message", "agent_message", "agent_thought", UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		return d.decodeMessage(sessionID, kind, fields)
	case "terminal_update", "terminal_output_chunk":
		return d.decodeTerminal(sessionID, kind, fields, raw)
	case "config_option_update":
		var config v2.SessionConfigOptionUpdate
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, err
		}
		return []Update{ConfigOptionUpdate{SessionUpdate: kind, ConfigOptions: draftConfigOptions(config.ConfigOptions)}}, nil
	case "plan_update":
		var plan v2.SessionPlanUpdate
		if err := json.Unmarshal(raw, &plan); err != nil {
			return nil, err
		}
		if plan.Plan.Items == nil {
			return []Update{RawUpdate{SessionUpdate: kind, Raw: append(json.RawMessage(nil), raw...)}}, nil
		}
		entries, err := draftConvert[[]PlanEntry](plan.Plan.Items.Entries)
		if err != nil {
			return nil, err
		}
		return []Update{PlanUpdate{SessionUpdate: UpdatePlan, Entries: entries}}, nil
	case "tool_call_update":
		var update ToolCallUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		update.RawPatch = make(map[string]json.RawMessage, len(fields))
		for key, value := range fields {
			update.RawPatch[key] = append(json.RawMessage(nil), value...)
		}
		d.mu.Lock()
		session := d.sessionLocked(sessionID)
		updates := []Update{update}
		for _, content := range update.Content {
			if content.Type == "terminal" && content.TerminalID != "" {
				terminal := session.terminals[content.TerminalID]
				if terminal == nil {
					if len(session.terminals) >= draftMaxSessionRecords {
						d.mu.Unlock()
						return nil, errors.New("acp draft: retained terminal identity limit exceeded")
					}
					terminal = &draftTerminal{info: make(map[string]any)}
					session.terminals[content.TerminalID] = terminal
				}
				firstReference := terminal.toolID == ""
				terminal.toolID = update.ToolCallID
				if firstReference && (len(terminal.info) != 0 || terminal.outputPresent || terminal.exitPresent) {
					meta := d.terminalMetaLocked(content.TerminalID, terminal)
					if terminal.outputPresent {
						meta[acpmeta.TerminalOutputKey] = map[string]any{"terminal_id": content.TerminalID, "data": string(terminal.output), "replacement": true}
					}
					if terminal.exitPresent {
						meta = draftTerminalExitMeta(meta, content.TerminalID, terminal.exit)
					}
					updates = append(updates, ToolCallUpdate{SessionUpdate: UpdateToolCallState, ToolCallID: update.ToolCallID, Meta: meta})
					terminal.output, terminal.outputPresent = nil, false
				}
				if err := d.accountTerminalLocked(session, content.TerminalID, terminal); err != nil {
					d.mu.Unlock()
					return nil, err
				}
			}
		}
		d.mu.Unlock()
		return updates, nil
	default:
		update, err := decodeUpdate(raw)
		if err != nil {
			return nil, err
		}
		if update == nil {
			return nil, nil
		}
		return []Update{update}, nil
	}
}

func (d *draftClient) decodeMessage(sessionID, kind string, fields map[string]json.RawMessage) ([]Update, error) {
	var messageID string
	_ = json.Unmarshal(fields["messageId"], &messageID)
	if strings.TrimSpace(messageID) == "" {
		return nil, errors.New("acp draft: message update has no identity")
	}
	replacement := !strings.HasSuffix(kind, "_chunk")
	baseKind := strings.TrimSuffix(kind, "_chunk")
	chunkKind := baseKind + "_chunk"
	d.mu.Lock()
	defer d.mu.Unlock()
	session := d.sessionLocked(sessionID)
	key := baseKind + "\x00" + messageID
	message := session.messages[key]
	newMessage := message == nil
	if newMessage {
		if len(session.messages) >= draftMaxSessionRecords {
			return nil, errors.New("acp draft: retained message identity limit exceeded")
		}
		message = &draftMessage{}
		session.messages[key] = message
	}
	_, metaPresent := fields["_meta"]
	if meta, present := fields["_meta"]; present {
		message.meta = nil
		if !bytes.Equal(bytes.TrimSpace(meta), []byte("null")) {
			if err := json.Unmarshal(meta, &message.meta); err != nil {
				return nil, err
			}
		}
	}
	if baseKind == "user_message" && newMessage {
		session.messageSequence++
	}
	if baseKind == "user_message" && newMessage && (session.prompt != nil || session.receiptWaiters > 0) {
		if len(session.userMessages) >= 4096 {
			return nil, errors.New("acp draft: too many unsettled message receipts")
		}
		session.userMessages[messageID] = session.messageSequence
		d.changedLocked(session)
	}
	content, present := fields["content"]
	if !present && (!metaPresent || !message.hasContent) {
		if err := d.accountMessageLocked(session, key, message); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var blocks []json.RawMessage
	if !present {
		// Content absence must remain absence: a metadata-only patch cannot
		// restore a progress message as the final answer after a tool barrier.
		if err := d.accountMessageLocked(session, key, message); err != nil {
			return nil, err
		}
		return []Update{ContentChunk{SessionUpdate: chunkKind, MessageID: messageID, Content: json.RawMessage(`{"type":"text","text":""}`), Meta: message.meta, MetaPresent: true}}, nil
	} else if replacement {
		if !bytes.Equal(bytes.TrimSpace(content), []byte("null")) {
			if err := json.Unmarshal(content, &blocks); err != nil {
				return nil, err
			}
		}
		message.content = append([]json.RawMessage(nil), blocks...)
	} else {
		blocks = []json.RawMessage{append(json.RawMessage(nil), content...)}
		message.content = append(message.content, blocks...)
		if session.narrativeKey != key {
			// Existing v1 presentation groups streaming runs. A standard v2
			// chunk instead addresses its durable message identity. Materialize
			// once on introduction/revisit/boundary, then preserve exact deltas
			// for contiguous chunks of this same message.
			blocks = append([]json.RawMessage(nil), message.content...)
			replacement = true
		}
	}
	message.hasContent = true
	session.narrativeKey = key
	if err := d.accountMessageLocked(session, key, message); err != nil {
		return nil, err
	}
	if len(blocks) == 0 && replacement {
		blocks = []json.RawMessage{json.RawMessage(`{"type":"text","text":""}`)}
	}
	updates := make([]Update, 0, len(blocks))
	for index, block := range blocks {
		updates = append(updates, ContentChunk{SessionUpdate: chunkKind, MessageID: messageID, Content: block, Meta: message.meta, Replacement: replacement && index == 0, MetaPresent: metaPresent && index == 0})
	}
	return updates, nil
}

func (d *draftClient) decodeTerminal(sessionID, kind string, fields map[string]json.RawMessage, raw json.RawMessage) ([]Update, error) {
	var id string
	_ = json.Unmarshal(fields["terminalId"], &id)
	if id == "" {
		return nil, errors.New("acp draft: terminal has no identity")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	session := d.sessionLocked(sessionID)
	terminal := session.terminals[id]
	if terminal == nil {
		if len(session.terminals) >= draftMaxSessionRecords {
			return nil, errors.New("acp draft: retained terminal identity limit exceeded")
		}
		terminal = &draftTerminal{info: make(map[string]any)}
		session.terminals[id] = terminal
	}
	var output []byte
	_, outputPresent := fields["output"]
	replacement := kind == "terminal_update" && outputPresent
	if kind == "terminal_output_chunk" {
		var data string
		if err := json.Unmarshal(fields["data"], &data); err != nil {
			return nil, err
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, err
		}
		output = decoded
		if terminal.toolID == "" {
			terminal.output = append(terminal.output, decoded...)
			terminal.outputPresent = true
		}
	} else {
		for _, key := range []string{"command", "cwd"} {
			if value, present := fields[key]; present {
				var decoded any
				if err := json.Unmarshal(value, &decoded); err != nil {
					return nil, err
				}
				if decoded == nil {
					delete(terminal.info, key)
				} else {
					terminal.info[key] = decoded
				}
			}
		}
		if rawOutput, present := fields["output"]; present {
			if terminal.toolID == "" {
				terminal.output, terminal.outputPresent = nil, true
			}
			if !bytes.Equal(bytes.TrimSpace(rawOutput), []byte("null")) {
				var snapshot v2.TerminalOutput
				if err := json.Unmarshal(rawOutput, &snapshot); err != nil {
					return nil, err
				}
				decoded, err := base64.StdEncoding.DecodeString(snapshot.Data)
				if err != nil {
					return nil, err
				}
				output = decoded
				if terminal.toolID == "" {
					terminal.output = decoded
				}
			}
		}
		if exit, present := fields["exitStatus"]; present {
			terminal.exit = nil
			terminal.exitPresent = true
			if !bytes.Equal(bytes.TrimSpace(exit), []byte("null")) {
				if err := json.Unmarshal(exit, &terminal.exit); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(output) > draftMaxSessionCacheBytes || len(terminal.output) > draftMaxSessionCacheBytes {
		return nil, errors.New("acp draft: terminal output frame exceeds bridge limit")
	}
	if err := d.accountTerminalLocked(session, id, terminal); err != nil {
		return nil, err
	}
	if terminal.toolID == "" {
		return []Update{RawUpdate{SessionUpdate: kind, Raw: append(json.RawMessage(nil), raw...)}}, nil
	}
	meta := d.terminalMetaLocked(id, terminal)
	if kind == "terminal_output_chunk" || outputPresent {
		// Chunks append only their own bytes. A complete snapshot (including
		// null/empty) replaces the existing terminal output in shared owners.
		values := map[string]any{"terminal_id": id, "data": string(output)}
		if replacement {
			values["replacement"] = true
		}
		meta[acpmeta.TerminalOutputKey] = values
	}
	if _, present := fields["exitStatus"]; present {
		meta = draftTerminalExitMeta(meta, id, terminal.exit)
	}
	return []Update{ToolCallUpdate{SessionUpdate: UpdateToolCallState, ToolCallID: terminal.toolID, Meta: meta}}, nil
}

func (d *draftClient) terminalMetaLocked(id string, terminal *draftTerminal) map[string]any {
	meta := acpmeta.WithTerminalInfo(nil, id)
	info := make(map[string]any, len(terminal.info)+1)
	for key, value := range terminal.info {
		info[key] = value
	}
	info["terminal_id"] = id
	meta[acpmeta.TerminalInfoKey] = info
	return meta
}

func draftTerminalExitMeta(meta map[string]any, id string, exit map[string]any) map[string]any {
	raw, _ := json.Marshal(exit)
	var status v2.TerminalExitStatus
	_ = json.Unmarshal(raw, &status)
	var code *int
	if status.ExitCode != nil {
		converted := int(*status.ExitCode)
		code = &converted
	}
	return acpmeta.WithTerminalExit(meta, id, code, status.Signal)
}
