package client

import (
	"encoding/json"
	"errors"
)

const (
	draftMaxSessionCacheBytes = 32 << 20
	draftMaxClientCacheBytes  = 128 << 20
	draftMaxSessionRecords    = 4096
	draftMaxSessions          = 1024
)

// The selected draft allows later patches to any retained message identity.
// Never evict identities and silently reinterpret a repair as a new message or
// Activity. Exhaustion closes the channel with an explicit unknown outcome.
func (d *draftClient) reserveCacheLocked(session *draftSession, size, previous int) error {
	delta := size - previous
	if session.cacheBytes+delta > draftMaxSessionCacheBytes || d.cacheBytes+delta > draftMaxClientCacheBytes {
		return errors.New("acp draft: retained display cache byte limit exceeded")
	}
	session.cacheBytes += delta
	d.cacheBytes += delta
	return nil
}

func (d *draftClient) accountMessageLocked(session *draftSession, key string, message *draftMessage) error {
	meta, err := json.Marshal(message.meta)
	if err != nil {
		return err
	}
	size := len(key) + len(meta) + 256
	for _, block := range message.content {
		size += len(block) + 32
	}
	if err := d.reserveCacheLocked(session, size, message.cacheBytes); err != nil {
		return err
	}
	message.cacheBytes = size
	return nil
}

func (d *draftClient) accountTerminalLocked(session *draftSession, id string, terminal *draftTerminal) error {
	info, err := json.Marshal(terminal.info)
	if err != nil {
		return err
	}
	exit, err := json.Marshal(terminal.exit)
	if err != nil {
		return err
	}
	size := len(id) + len(terminal.toolID) + len(info) + len(exit) + len(terminal.output) + 256
	if err := d.reserveCacheLocked(session, size, terminal.cacheBytes); err != nil {
		return err
	}
	terminal.cacheBytes = size
	return nil
}

type draftTool struct {
	meta       map[string]any
	cacheBytes int
}

// observeToolLocked retains emitted tool identity and draft metadata in the
// bounded Session cache. Execution and approval state live elsewhere.
func (d *draftClient) observeToolLocked(session *draftSession, id string) (bool, error) {
	if _, known := session.tools[id]; known {
		return true, nil
	}
	if len(session.tools) >= draftMaxSessionRecords {
		return false, errors.New("acp draft: retained tool identity limit exceeded")
	}
	if err := d.reserveCacheLocked(session, len(id)+128, 0); err != nil {
		return false, err
	}
	if session.tools == nil {
		session.tools = make(map[string]*draftTool)
	}
	session.tools[id] = &draftTool{cacheBytes: len(id) + 128}
	return false, nil
}

func (d *draftClient) patchToolMetaLocked(session *draftSession, id string, raw json.RawMessage) (map[string]any, error) {
	tool := session.tools[id]
	meta, err := draftPatchMeta(tool.meta, raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	size := len(id) + len(encoded) + 128
	if err := d.reserveCacheLocked(session, size, tool.cacheBytes); err != nil {
		return nil, err
	}
	tool.meta, tool.cacheBytes = meta, size
	return meta, nil
}
