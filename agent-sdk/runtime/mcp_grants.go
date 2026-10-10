package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// MCPGrantStore is the Host-owned authority for approved MCP tool scopes. One
// instance must be shared by all Runtime activations in a Host. The file is
// private to the Host Store; deleting it while the Host is stopped revokes all
// grants. Revoke removes one grant while the Host is running.
type MCPGrantStore struct {
	mu     sync.Mutex
	path   string
	grants map[string]bool
}

// NewMCPGrantStore opens a private, versioned grant file. Corrupt state fails
// closed rather than resetting previously approved scopes silently.
func NewMCPGrantStore(path string) (*MCPGrantStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("MCP grant path is required")
	}
	s := &MCPGrantStore{path: path, grants: map[string]bool{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Version int             `json:"version"`
		Grants  map[string]bool `json:"grants"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 {
		return nil, fmt.Errorf("invalid MCP grant file: %w", errors.Join(err, errors.New("unsupported version or encoding")))
	}
	if doc.Grants != nil {
		s.grants = doc.Grants
	}
	return s, nil
}

// MCPGrant identifies exactly one ready MCP tool, workspace, and trusted
// configuration owner. The source fingerprint binds the accepted server
// configuration and remote definition; a connection, tool, or schema change
// does not reuse an earlier grant.
type MCPGrant struct {
	// Owner is supplied by the embedding Host, never by a tool or approval
	// response. Empty selects the ordinary Host configuration owner.
	Owner     string
	Workspace string
	PluginID  string
	Server    string
	Tool      string
	Projected string
	Source    string
	// SessionEpoch prevents a newly created Session from inheriting a grant
	// if an embedding ever reuses the same Session ID.
	SessionEpoch string
}

func mcpGrantFor(def tool.Definition, active session.Session) (MCPGrant, error) {
	field := func(key string) string {
		value, _ := def.Metadata[key].(string)
		return strings.TrimSpace(value)
	}
	grant := MCPGrant{
		Workspace:    strings.TrimSpace(active.CWD),
		PluginID:     field(tool.MetadataPluginID),
		Server:       field(tool.MetadataMCPServer),
		Tool:         field(tool.MetadataMCPTool),
		Projected:    strings.TrimSpace(def.Name),
		Source:       field(tool.MetadataMCPSourceFingerprint),
		SessionEpoch: active.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if grant.Workspace == "" || grant.Server == "" || grant.Tool == "" || grant.Projected == "" || grant.Source == "" {
		return MCPGrant{}, errors.New("MCP tool source identity is incomplete")
	}
	return grant, nil
}

func (g MCPGrant) key(scope, sessionID string) (string, error) {
	if scope != "session" && scope != "always" {
		return "", errors.New("invalid MCP grant scope")
	}
	if scope == "session" && (strings.TrimSpace(sessionID) == "" || g.SessionEpoch == "" || g.SessionEpoch == time.Time{}.UTC().Format(time.RFC3339Nano)) {
		return "", errors.New("session MCP grant requires a Session ID and creation time")
	}
	if g.Workspace == "" || g.Server == "" || g.Tool == "" || g.Projected == "" || g.Source == "" {
		return "", errors.New("incomplete MCP grant")
	}
	epoch := ""
	if scope == "session" {
		epoch = g.SessionEpoch
	}
	owner := strings.TrimSpace(g.Owner)
	if owner == "" {
		owner = "host"
	}
	raw, err := json.Marshal([]string{scope, sessionID, epoch, owner, g.Workspace, g.PluginID, g.Server, g.Tool, g.Projected, g.Source})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return scope + ":" + hex.EncodeToString(hash[:]), nil
}

// Allows reports whether this Session or the durable workspace-wide scope
// covers the exact tool source. Storage errors must prevent execution.
func (s *MCPGrantStore) Allows(g MCPGrant, sessionID string) (bool, error) {
	if s == nil {
		return false, nil
	}
	global, err := g.key("always", "")
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.grants[global] {
		return true, nil
	}
	current, err := g.key("session", sessionID)
	if err != nil {
		// A legacy Session without creation time can still use Allow Once;
		// it cannot create or consume a Session-scoped grant.
		return false, nil
	}
	return s.grants[current], nil
}

// Grant commits a selected Session or Always scope before the remote MCP call.
func (s *MCPGrantStore) Grant(g MCPGrant, scope, sessionID string) error {
	if s == nil {
		return errors.New("MCP grant store is unavailable")
	}
	key, err := g.key(scope, sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.change(key, true)
}

// Revoke removes one precise grant. It is the live revocation path for Host
// control code; a subsequent invocation must obtain fresh approval.
func (s *MCPGrantStore) Revoke(g MCPGrant, scope, sessionID string) error {
	if s == nil {
		return errors.New("MCP grant store is unavailable")
	}
	key, err := g.key(scope, sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.change(key, false)
}

func (s *MCPGrantStore) change(key string, present bool) error {
	next := make(map[string]bool, len(s.grants)+1)
	for existing, granted := range s.grants {
		if granted {
			next[existing] = true
		}
	}
	if present {
		next[key] = true
	} else {
		delete(next, key)
	}
	raw, err := json.Marshal(struct {
		Version int             `json:"version"`
		Grants  map[string]bool `json:"grants"`
	}{1, next})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".mcp-grants-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	s.grants = next
	return nil
}
