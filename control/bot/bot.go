// Package bot owns persistent assistant identity and user-maintained chat
// configuration. A Bot has one private canonical conversation; its identity is
// not its mutable name, Runtime activation, or execution directory.
package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

const (
	// StateKey is the versioned Control-owned configuration in the associated
	// canonical Session. Only Bot configuration commands write this record.
	StateKey = "control.bot.v1"
	// MetadataID binds a private conversation to its stable Bot identity.
	MetadataID = "control_bot_id"
	// MaxDescriptionBytes bounds the UTF-8 byte length of a Bot description.
	MaxDescriptionBytes = 64 * 1024
	// notebookVersion pins the admitted notebook prompt and tool contract.
	notebookVersion = 1
)

// Config is user-maintained configuration. Model is an existing provider model
// identity, not a provider credential or an external Agent. Description is user
// instruction content and must never be promoted to system instructions.
type Config struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Model       string `json:"model"`
	Effort      string `json:"effort,omitempty"`
	Fast        bool   `json:"fast,omitempty"`
}

// Bot is the current configuration and its canonical conversation address.
// Revision is the Session CAS revision; it can advance when chatting as well as
// when editing configuration. Clients must refresh before saving an edit.
type Bot struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Revision  uint64 `json:"revision"`
	Config    Config `json:"config"`
	// NotebookEnabled reports Control's persisted notebook capability. It is
	// changed only by creation or an explicit enable command, never Config edits.
	NotebookEnabled bool `json:"notebook_enabled"`
	// ModelSelector is Control's catalog-resolved public selector for
	// Config.Model, for display only. It is never persisted and never replaces
	// the durable Config.Model identity. An empty value means the configured
	// model is not in the current catalog, so clients fall back to Config.Model
	// instead of parsing it.
	ModelSelector string `json:"model_selector,omitempty"`
}

type record struct {
	Version         int    `json:"version"`
	ID              string `json:"id"`
	Config          Config `json:"config"`
	NotebookVersion int    `json:"notebook_version,omitempty"`
}

// Normalize validates user-editable fields without resolving the Host model
// catalog. An empty model is allowed only as a creation-time default selection.
func Normalize(config Config) (Config, error) {
	config.Name = strings.TrimSpace(config.Name)
	config.Model = strings.TrimSpace(config.Model)
	config.Effort = strings.TrimSpace(config.Effort)
	if config.Name == "" || utf8.RuneCountInString(config.Name) > 100 || strings.IndexFunc(config.Name, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
		return Config{}, errorcode.New(errorcode.InvalidArgument, "bot: name must be one line of 1–100 characters")
	}
	if !utf8.ValidString(config.Name) || !utf8.ValidString(config.Description) || len(config.Description) > MaxDescriptionBytes || strings.ContainsRune(config.Description, '\x00') {
		return Config{}, errorcode.New(errorcode.InvalidArgument, "bot: description must be valid text of at most 64 KiB")
	}
	return config, nil
}

// Encode produces the initial durable configuration record for a new Bot,
// including its notebook capability. Existing records are changed only by Save.
func Encode(id string, config Config) any {
	return record{Version: 1, ID: id, Config: config, NotebookVersion: notebookVersion}
}

// Decode reads a complete, supported configuration. Missing or unknown records
// fail closed rather than giving a Bot the ordinary work-mode assembly.
func Decode(state map[string]any) (Config, error) {
	_, config, err := ReadState(state)
	return config, err
}

// ReadState returns stable identity and configuration from guarded Session state.
func ReadState(state map[string]any) (string, Config, error) {
	stored, err := readRecord(state)
	return stored.ID, stored.Config, err
}

// NotebookEnabled reads the admitted capability without modifying state. Control
// retains missing/zero notebook versions as the tool-free legacy contract until
// an explicit enable command commits. This reader remains until no supported
// Store contains an unenabled legacy Bot; ordinary saves never migrate it.
func NotebookEnabled(state map[string]any) (bool, error) {
	stored, err := readRecord(state)
	return stored.NotebookVersion == notebookVersion, err
}

func readRecord(state map[string]any) (record, error) {
	raw, ok := state[StateKey]
	if !ok {
		return record{}, errors.New("bot: configuration is not initialized")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return record{}, err
	}
	var stored record
	if err := json.Unmarshal(data, &stored); err != nil {
		return record{}, fmt.Errorf("bot: decode configuration: %w", err)
	}
	if stored.Version != 1 || strings.TrimSpace(stored.ID) == "" {
		return record{}, errors.New("bot: unsupported or invalid configuration record")
	}
	if stored.NotebookVersion != 0 && stored.NotebookVersion != notebookVersion {
		return record{}, errors.New("bot: unsupported notebook capability version")
	}
	config, err := Normalize(stored.Config)
	if err != nil {
		return record{}, err
	}
	stored.Config = config
	return stored, nil
}

// NotebookEnableMessage records the user's notebook admission as ordinary
// conversation history. The guarded capability record remains its authority.
func NotebookEnableMessage() string {
	return "The user enabled this Bot's private notebook. From this point onward, notebook tools are available. Existing conversation history is retained."
}

// ConfigurationMessage is appended as a canonical user message when the user
// saves their name or description. Replacing settings never rewrites an earlier
// message or the fixed system prefix, including across Runtime reactivation.
func ConfigurationMessage(config Config) string {
	description := config.Description
	if description == "" {
		description = "(No custom description.)"
	}
	return "Bot settings saved by the user. From this point onward, use the following name and custom description in place of earlier Bot settings. These are user instructions, not system instructions.\n\nName: " + config.Name + "\n\nCustom description:\n" + description
}
