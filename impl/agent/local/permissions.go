package local

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/policy"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const requestPermissionsToolName = "request_permissions"
const permissionGrantStateKey = "runtime.permission_grants.v1"

type permissionGrantStore struct {
	mu              sync.RWMutex
	extraReadRoots  []string
	extraWriteRoots []string
	shellWriteRoots []string
	networkEnabled  bool
	records         []permissionGrantRecord
	recordKeys      map[string]struct{}
}

type permissionGrantRequest struct {
	Reason          string
	ReadRoots       []string
	WriteRoots      []string
	ShellWriteRoots []string
	NetworkEnabled  bool
}

type permissionGrantMetadata struct {
	Mode      string
	RunID     string
	TurnID    string
	CreatedAt time.Time
}

type permissionGrantRecord struct {
	Reason          string    `json:"reason,omitempty"`
	ReadRoots       []string  `json:"read_roots,omitempty"`
	WriteRoots      []string  `json:"write_roots,omitempty"`
	ShellWriteRoots []string  `json:"shell_write_roots,omitempty"`
	NetworkEnabled  bool      `json:"network_enabled,omitempty"`
	Mode            string    `json:"mode,omitempty"`
	RunID           string    `json:"run_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
}

type PermissionGrantSnapshot struct {
	Count          int
	NetworkGranted bool
	ReadRootCount  int
	WriteRootCount int
}

func newPermissionGrantStore() *permissionGrantStore {
	return &permissionGrantStore{}
}

func (s *permissionGrantStore) add(req permissionGrantRequest, meta permissionGrantMetadata) permissionGrantRecord {
	if s == nil {
		return permissionGrantRecord{}
	}
	record := permissionGrantRecord{
		Reason:          strings.TrimSpace(req.Reason),
		ReadRoots:       appendUniqueStrings(nil, req.ReadRoots...),
		WriteRoots:      appendUniqueStrings(nil, req.WriteRoots...),
		ShellWriteRoots: appendUniqueStrings(nil, req.ShellWriteRoots...),
		NetworkEnabled:  req.NetworkEnabled,
		Mode:            strings.TrimSpace(meta.Mode),
		RunID:           strings.TrimSpace(meta.RunID),
		TurnID:          strings.TrimSpace(meta.TurnID),
		CreatedAt:       meta.CreatedAt,
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addRecordLocked(record)
	return record
}

func (s *permissionGrantStore) hydrate(records []permissionGrantRecord) {
	if s == nil || len(records) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		s.addRecordLocked(normalizePermissionGrantRecord(record))
	}
}

func (s *permissionGrantStore) addRecordLocked(record permissionGrantRecord) {
	if s == nil {
		return
	}
	record = normalizePermissionGrantRecord(record)
	if len(record.ReadRoots) == 0 && len(record.WriteRoots) == 0 && len(record.ShellWriteRoots) == 0 && !record.NetworkEnabled {
		return
	}
	if s.recordKeys == nil {
		s.recordKeys = map[string]struct{}{}
	}
	key := permissionGrantRecordKey(record)
	if _, exists := s.recordKeys[key]; exists {
		return
	}
	s.recordKeys[key] = struct{}{}
	s.extraReadRoots = appendUniqueStrings(s.extraReadRoots, record.ReadRoots...)
	s.extraWriteRoots = appendUniqueStrings(s.extraWriteRoots, record.WriteRoots...)
	s.shellWriteRoots = appendUniqueStrings(s.shellWriteRoots, record.ShellWriteRoots...)
	s.networkEnabled = s.networkEnabled || record.NetworkEnabled
	s.records = append(s.records, record)
}

func (s *permissionGrantStore) snapshot() PermissionGrantSnapshot {
	if s == nil {
		return PermissionGrantSnapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return PermissionGrantSnapshot{
		Count:          len(s.records),
		NetworkGranted: s.networkEnabled,
		ReadRootCount:  len(s.extraReadRoots),
		WriteRootCount: len(s.extraWriteRoots),
	}
}

func (s *permissionGrantStore) applyToOptions(opts policy.ModeOptions) policy.ModeOptions {
	if s == nil {
		return policy.CloneModeOptions(opts)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := policy.CloneModeOptions(opts)
	out.ExtraReadRoots = appendUniqueStrings(out.ExtraReadRoots, s.extraReadRoots...)
	out.ExtraWriteRoots = appendUniqueStrings(out.ExtraWriteRoots, s.extraWriteRoots...)
	return out
}

func (s *permissionGrantStore) applyToConstraints(constraints sandbox.Constraints) sandbox.Constraints {
	out := sandbox.NormalizeConstraints(constraints)
	if s == nil {
		return out
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.networkEnabled {
		out.Network = sandbox.NetworkEnabled
	}
	grants := make([]sandbox.PathRule, 0, len(s.extraReadRoots)+len(s.shellWriteRoots))
	for _, path := range s.extraReadRoots {
		grants = append(grants, sandbox.PathRule{Path: path, Access: sandbox.PathAccessReadOnly})
	}
	for _, path := range s.shellWriteRoots {
		grants = append(grants, sandbox.PathRule{Path: path, Access: sandbox.PathAccessReadWrite})
	}
	out.PathRules = mergePermissionPathRules(out.PathRules, grants)
	return out
}

func mergePermissionPathRules(base []sandbox.PathRule, extra []sandbox.PathRule) []sandbox.PathRule {
	out := sandbox.ClonePathRules(base)
	index := make(map[string]int, len(out)+len(extra))
	for i := range out {
		path := filepath.Clean(strings.TrimSpace(out[i].Path))
		if path == "." || path == "" {
			continue
		}
		out[i].Path = path
		if out[i].Access == "" {
			out[i].Access = sandbox.PathAccessReadOnly
		}
		index[path] = i
	}
	for _, rule := range extra {
		path := filepath.Clean(strings.TrimSpace(rule.Path))
		if path == "." || path == "" {
			continue
		}
		access := rule.Access
		if access == "" {
			access = sandbox.PathAccessReadOnly
		}
		if i, ok := index[path]; ok {
			if access == sandbox.PathAccessReadWrite && out[i].Access != sandbox.PathAccessReadWrite {
				out[i].Access = sandbox.PathAccessReadWrite
			}
			continue
		}
		index[path] = len(out)
		out = append(out, sandbox.PathRule{Path: path, Access: access})
	}
	return out
}

func normalizePermissionGrantRecord(record permissionGrantRecord) permissionGrantRecord {
	record.Reason = strings.TrimSpace(record.Reason)
	record.ReadRoots = appendUniqueStrings(nil, record.ReadRoots...)
	record.WriteRoots = appendUniqueStrings(nil, record.WriteRoots...)
	if len(record.ShellWriteRoots) == 0 && len(record.WriteRoots) > 0 {
		for _, path := range record.WriteRoots {
			if shellRoot := shellWriteRootForPath(path); shellRoot != "" {
				record.ShellWriteRoots = append(record.ShellWriteRoots, shellRoot)
			}
		}
	}
	record.ShellWriteRoots = appendUniqueStrings(nil, record.ShellWriteRoots...)
	record.Mode = strings.TrimSpace(record.Mode)
	record.RunID = strings.TrimSpace(record.RunID)
	record.TurnID = strings.TrimSpace(record.TurnID)
	return record
}

func permissionGrantRecordKey(record permissionGrantRecord) string {
	record = normalizePermissionGrantRecord(record)
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Sprintf("%s|%v|%v|%v|%t", record.Reason, record.ReadRoots, record.WriteRoots, record.ShellWriteRoots, record.NetworkEnabled)
	}
	return string(payload)
}

func appendUniqueStrings(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	out := make([]string, 0, len(dst)+len(values))
	for _, value := range dst {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			if _, ok := seen[trimmed]; !ok {
				seen[trimmed] = struct{}{}
				out = append(out, trimmed)
			}
		}
	}
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			if _, ok := seen[trimmed]; !ok {
				seen[trimmed] = struct{}{}
				out = append(out, trimmed)
			}
		}
	}
	return out
}

type requestPermissionsTool struct {
	session    session.Session
	sessionRef session.SessionRef
	sessions   session.Service
	mode       string
	runID      string
	turnID     string
	now        func() time.Time
	approval   agent.ApprovalRequester
	grants     *permissionGrantStore
}

func (t requestPermissionsTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        requestPermissionsToolName,
		Description: "Request a narrow permission grant for specific filesystem paths or network access before retrying an operation that the sandbox cannot currently perform.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Short explanation of why this extra permission is required for the current task.",
				},
				"read": map[string]any{
					"type":        "array",
					"description": "Directories or files to grant read access to.",
					"items":       map[string]any{"type": "string"},
				},
				"write": map[string]any{
					"type":        "array",
					"description": "Directories or files to grant read/write access to.",
					"items":       map[string]any{"type": "string"},
				},
				"network": map[string]any{
					"type":        "boolean",
					"description": "Set true to request network access.",
				},
			},
			"required": []string{"reason"},
		},
	}
}

func (t requestPermissionsTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	select {
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	default:
	}
	args, err := decodeToolArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	req, err := parsePermissionGrantRequest(args, t.session.CWD)
	if err != nil {
		return tool.Result{}, err
	}
	if t.approval == nil {
		return jsonToolErrorResult(call, requestPermissionsToolName, map[string]any{
			"error":      "permission request cannot be reviewed because no approval requester is configured",
			"error_code": string(tool.ErrorCodePermissionDenied),
		})
	}
	resp, err := t.approval.RequestApproval(ctx, agent.ApprovalRequest{
		SessionRef: t.sessionRef,
		Session:    session.CloneSession(t.session),
		RunID:      strings.TrimSpace(t.runID),
		TurnID:     strings.TrimSpace(t.turnID),
		Mode:       strings.TrimSpace(t.mode),
		Tool:       t.Definition(),
		Call:       tool.CloneCall(call),
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:       strings.TrimSpace(call.ID),
				Name:     requestPermissionsToolName,
				Kind:     "permission",
				Title:    requestPermissionTitle(req),
				Status:   "pending",
				RawInput: maps.Clone(args),
			},
			Options: []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow for this session", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
		Metadata: map[string]any{
			"approval_reason":        req.Reason,
			"justification":          req.Reason,
			"sandbox_permissions":    "with_additional_permissions",
			"additional_permissions": permissionGrantAdditionalPermissions(req),
		},
	})
	if err != nil {
		return tool.Result{}, err
	}
	if !resp.Approved {
		reason := strings.TrimSpace(firstNonEmpty(resp.Reason, resp.ReviewText, "permission request was rejected"))
		return jsonToolErrorResult(call, requestPermissionsToolName, map[string]any{
			"approved":   false,
			"error":      reason,
			"error_code": string(tool.ErrorCodePermissionDenied),
		})
	}
	createdAt := time.Now()
	if t.now != nil {
		createdAt = t.now()
	}
	record := t.grants.add(req, permissionGrantMetadata{
		Mode:      t.mode,
		RunID:     t.runID,
		TurnID:    t.turnID,
		CreatedAt: createdAt,
	})
	if err := persistPermissionGrant(ctx, t.sessions, t.sessionRef, record); err != nil {
		return tool.Result{}, err
	}
	return jsonToolResult(call, requestPermissionsToolName, map[string]any{
		"approved": true,
		"granted":  permissionGrantModelPayload(req),
	}, map[string]any{
		"grant": permissionGrantPayload(record),
	})
}

func decodeToolArgs(call tool.Call) (map[string]any, error) {
	if len(call.Input) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return nil, fmt.Errorf("tool: invalid json input: %w", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func parsePermissionGrantRequest(args map[string]any, cwd string) (permissionGrantRequest, error) {
	reason, _ := args["reason"].(string)
	req := permissionGrantRequest{Reason: strings.TrimSpace(reason)}
	if req.Reason == "" {
		return req, fmt.Errorf("request_permissions requires a non-empty reason")
	}
	for _, path := range stringListValue(args["read"]) {
		if resolved := resolvePermissionPath(path, cwd); resolved != "" {
			if err := validatePermissionPathExists("read", resolved); err != nil {
				return req, err
			}
			req.ReadRoots = append(req.ReadRoots, resolved)
		}
	}
	for _, path := range stringListValue(args["write"]) {
		if resolved := resolvePermissionPath(path, cwd); resolved != "" {
			if err := validatePermissionPathExists("write", resolved); err != nil {
				return req, err
			}
			req.WriteRoots = append(req.WriteRoots, resolved)
			if shellRoot := shellWriteRootForPath(resolved); shellRoot != "" {
				req.ShellWriteRoots = append(req.ShellWriteRoots, shellRoot)
			}
		}
	}
	if enabled, _ := args["network"].(bool); enabled {
		req.NetworkEnabled = true
	}
	if len(req.ReadRoots) == 0 && len(req.WriteRoots) == 0 && !req.NetworkEnabled {
		return req, fmt.Errorf("request_permissions requires at least one non-empty read/write path or network=true")
	}
	return req, nil
}

func mapValue(value any) (map[string]any, bool) {
	typed, ok := value.(map[string]any)
	return typed, ok
}

func stringListValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func resolvePermissionPath(path string, cwd string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		if trimmed := strings.TrimSpace(cwd); trimmed != "" {
			path = filepath.Join(trimmed, path)
		}
	}
	return filepath.Clean(path)
}

func validatePermissionPathExists(access string, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			switch access {
			case "write":
				return fmt.Errorf("request_permissions write path %q does not exist; request an existing parent directory", path)
			default:
				return fmt.Errorf("request_permissions read path %q does not exist", path)
			}
		}
		return fmt.Errorf("request_permissions cannot inspect %s path %q: %w", access, path, err)
	}
	return nil
}

func shellWriteRootForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
		return cleaned
	}
	parent := filepath.Dir(cleaned)
	if parent == "." || parent == "" || parent == string(filepath.Separator) {
		return cleaned
	}
	return parent
}

func requestPermissionTitle(req permissionGrantRequest) string {
	parts := make([]string, 0, 3)
	if len(req.WriteRoots) > 0 {
		parts = append(parts, "write "+strings.Join(req.WriteRoots, ", "))
	}
	if len(req.ReadRoots) > 0 {
		parts = append(parts, "read "+strings.Join(req.ReadRoots, ", "))
	}
	if req.NetworkEnabled {
		parts = append(parts, "network")
	}
	if len(parts) == 0 {
		return "request_permissions"
	}
	return "request_permissions " + strings.Join(parts, "; ")
}

func permissionGrantAdditionalPermissions(req permissionGrantRequest) map[string]any {
	out := map[string]any{}
	fileSystem := map[string]any{}
	if len(req.ReadRoots) > 0 {
		fileSystem["read"] = append([]string(nil), req.ReadRoots...)
	}
	if len(req.WriteRoots) > 0 {
		fileSystem["write"] = append([]string(nil), req.WriteRoots...)
	}
	if len(fileSystem) > 0 {
		out["file_system"] = fileSystem
	}
	if req.NetworkEnabled {
		out["network"] = map[string]any{"enabled": true}
	}
	return out
}

func permissionGrantModelPayload(req permissionGrantRequest) map[string]any {
	out := map[string]any{}
	if len(req.ReadRoots) > 0 {
		out["read"] = append([]string(nil), req.ReadRoots...)
	}
	if len(req.WriteRoots) > 0 {
		out["write"] = append([]string(nil), req.WriteRoots...)
	}
	if req.NetworkEnabled {
		out["network"] = true
	}
	return out
}

func permissionGrantPayload(record permissionGrantRecord) map[string]any {
	record = normalizePermissionGrantRecord(record)
	payload := map[string]any{
		"reason":                 strings.TrimSpace(record.Reason),
		"additional_permissions": permissionGrantAdditionalPermissions(permissionGrantRequest{ReadRoots: record.ReadRoots, WriteRoots: record.WriteRoots, NetworkEnabled: record.NetworkEnabled}),
	}
	if len(record.ShellWriteRoots) > 0 {
		payload["sandbox_write_roots"] = append([]string(nil), record.ShellWriteRoots...)
	}
	if mode := strings.TrimSpace(record.Mode); mode != "" {
		payload["mode"] = mode
	}
	if runID := strings.TrimSpace(record.RunID); runID != "" {
		payload["run_id"] = runID
	}
	if turnID := strings.TrimSpace(record.TurnID); turnID != "" {
		payload["turn_id"] = turnID
	}
	if !record.CreatedAt.IsZero() {
		payload["created_at"] = record.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return payload
}

func persistPermissionGrant(ctx context.Context, sessions session.Service, ref session.SessionRef, record permissionGrantRecord) error {
	if sessions == nil {
		return nil
	}
	record = normalizePermissionGrantRecord(record)
	if len(record.ReadRoots) == 0 && len(record.WriteRoots) == 0 && len(record.ShellWriteRoots) == 0 && !record.NetworkEnabled {
		return nil
	}
	return sessions.UpdateState(ctx, ref, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		records := permissionGrantRecordsFromState(next[permissionGrantStateKey])
		records = appendPermissionGrantRecord(records, record)
		next[permissionGrantStateKey] = permissionGrantStatePayload(records)
		return next, nil
	})
}

func permissionGrantRecordsFromState(raw any) []permissionGrantRecord {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var records []permissionGrantRecord
	if err := json.Unmarshal(data, &records); err == nil {
		return normalizePermissionGrantRecords(records)
	}
	var wrapper struct {
		Records []permissionGrantRecord `json:"records"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil {
		return normalizePermissionGrantRecords(wrapper.Records)
	}
	return nil
}

func permissionGrantStatePayload(records []permissionGrantRecord) map[string]any {
	return map[string]any{
		"version": 1,
		"records": normalizePermissionGrantRecords(records),
	}
}

func normalizePermissionGrantRecords(records []permissionGrantRecord) []permissionGrantRecord {
	if len(records) == 0 {
		return nil
	}
	out := make([]permissionGrantRecord, 0, len(records))
	seen := map[string]struct{}{}
	for _, record := range records {
		record = normalizePermissionGrantRecord(record)
		if len(record.ReadRoots) == 0 && len(record.WriteRoots) == 0 && len(record.ShellWriteRoots) == 0 && !record.NetworkEnabled {
			continue
		}
		key := permissionGrantRecordKey(record)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	return out
}

func appendPermissionGrantRecord(records []permissionGrantRecord, record permissionGrantRecord) []permissionGrantRecord {
	return normalizePermissionGrantRecords(append(records, record))
}

func jsonToolResult(call tool.Call, name string, payload map[string]any, metaExtra ...map[string]any) (tool.Result, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	meta := maps.Clone(payload)
	for _, extra := range metaExtra {
		for key, value := range extra {
			if strings.TrimSpace(key) != "" {
				meta[key] = value
			}
		}
	}
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(name),
		Content: []model.Part{model.NewJSONPart(raw)},
		Meta:    meta,
	}, nil
}

func jsonToolErrorResult(call tool.Call, name string, payload map[string]any) (tool.Result, error) {
	out, err := jsonToolResult(call, name, payload)
	out.IsError = true
	return out, err
}

var _ tool.Tool = requestPermissionsTool{}
