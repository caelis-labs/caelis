package gatewayapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
)

// pluginOperationReceipt is an operation-attributable domain receipt for Host
// plugin mutations that perform external install/update effects. Recovery may
// prove a terminal result from this record without repeating those effects.
type pluginOperationReceipt struct {
	PrincipalID  string            `json:"principal_id"`
	OperationID  string            `json:"operation_id"`
	Digest       string            `json:"digest"`
	Action       appserver.Action  `json:"action"`
	Outcome      appserver.Outcome `json:"outcome"`
	Revision     uint64            `json:"revision,omitempty"`
	Detail       string            `json:"detail,omitempty"`
	ResourceKind string            `json:"resource_kind,omitempty"`
	Target       string            `json:"target,omitempty"`
	RecordedAt   time.Time         `json:"recorded_at"`
}

func (s *controlCommandBackend) pluginOperationReceiptDir() string {
	if s == nil || strings.TrimSpace(s.composition.authorities.storeDir) == "" {
		return ""
	}
	return filepath.Join(s.composition.authorities.storeDir, "plugins", "operation-receipts")
}

func pluginOperationReceiptPath(dir, principalID, operationID string) (string, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return "", errors.New("gatewayapp: plugin operation receipt directory is unavailable")
	}
	key := strings.TrimSpace(principalID) + "\x00" + strings.TrimSpace(operationID)
	if strings.TrimSpace(principalID) == "" || strings.TrimSpace(operationID) == "" {
		return "", errors.New("gatewayapp: plugin operation receipt identity is required")
	}
	sum := sha256.Sum256([]byte(key))
	name := hex.EncodeToString(sum[:]) + ".json"
	path := filepath.Join(dir, name)
	// Containment guard against any future path construction regressions.
	if filepath.Dir(filepath.Clean(path)) != dir {
		return "", errors.New("gatewayapp: plugin operation receipt path escapes store")
	}
	return path, nil
}

func (s *controlCommandBackend) writePluginOperationReceipt(ctx context.Context, receipt pluginOperationReceipt) error {
	if s == nil {
		return errors.New("gatewayapp: plugin operation receipt store is unavailable")
	}
	if err := contextOrBackground(ctx).Err(); err != nil {
		return err
	}
	dir := s.pluginOperationReceiptDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	receipt.PrincipalID = strings.TrimSpace(receipt.PrincipalID)
	receipt.OperationID = strings.TrimSpace(receipt.OperationID)
	receipt.Digest = strings.TrimSpace(receipt.Digest)
	if receipt.PrincipalID == "" || receipt.OperationID == "" || receipt.Digest == "" {
		return errors.New("gatewayapp: plugin operation receipt identity is required")
	}
	if receipt.RecordedAt.IsZero() {
		receipt.RecordedAt = time.Now().UTC()
	}
	path, err := pluginOperationReceiptPath(dir, receipt.PrincipalID, receipt.OperationID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *controlCommandBackend) loadPluginOperationReceipt(ctx context.Context, principalID, operationID, digest string) (pluginOperationReceipt, bool, error) {
	if s == nil {
		return pluginOperationReceipt{}, false, errors.New("gatewayapp: plugin operation receipt store is unavailable")
	}
	if err := contextOrBackground(ctx).Err(); err != nil {
		return pluginOperationReceipt{}, false, err
	}
	principalID = strings.TrimSpace(principalID)
	operationID = strings.TrimSpace(operationID)
	digest = strings.TrimSpace(digest)
	if principalID == "" || operationID == "" || digest == "" {
		return pluginOperationReceipt{}, false, nil
	}
	path, err := pluginOperationReceiptPath(s.pluginOperationReceiptDir(), principalID, operationID)
	if err != nil {
		return pluginOperationReceipt{}, false, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return pluginOperationReceipt{}, false, nil
	}
	if err != nil {
		return pluginOperationReceipt{}, false, err
	}
	var receipt pluginOperationReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return pluginOperationReceipt{}, false, fmt.Errorf("gatewayapp: decode plugin operation receipt: %w", err)
	}
	if strings.TrimSpace(receipt.PrincipalID) != principalID ||
		strings.TrimSpace(receipt.OperationID) != operationID ||
		strings.TrimSpace(receipt.Digest) != digest {
		return pluginOperationReceipt{}, false, nil
	}
	return receipt, true, nil
}

func pluginCommandResultFromReceipt(receipt pluginOperationReceipt) appserver.CommandResult {
	result := appserver.CommandResult{
		OperationID: receipt.OperationID,
		Outcome:     receipt.Outcome,
		Revision:    receipt.Revision,
		Detail:      receipt.Detail,
	}
	kind := strings.TrimSpace(receipt.ResourceKind)
	if kind == "" {
		kind = resourceKindForPluginAction(receipt.Action)
	}
	if target := strings.TrimSpace(receipt.Target); target != "" || kind != "" {
		result.Resource = &appserver.CommandResource{
			Kind: kind,
			Ref:  target,
		}
	}
	return result
}
