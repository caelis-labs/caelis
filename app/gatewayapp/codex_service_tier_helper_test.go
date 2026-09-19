package gatewayapp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/adapters/codex"
)

// codexTierHelperMarker selects this test process as the ACP endpoint used by
// the service-tier integration tests. The helper runs the real Codex adapter
// (codex.NewBackend + Backend.ServeACP) over stdio against an in-process fake
// Codex app-server, so one child process reproduces the production adapter
// without a Codex installation.
const codexTierHelperMarker = "caelis-codex-tier-helper"

const (
	// codexTierTieredID advertises Fast and Standard and defaults to Fast.
	codexTierTieredID = "gpt-a"
	// codexTierTierlessID advertises no additional tier.
	codexTierTierlessID = "gpt-b"
	// codexTierUnconfiguredID has a profile with no speed capability.
	codexTierUnconfiguredID = "gpt-c"

	codexTierPriority = "priority"
	codexTierStandard = "default"
	codexTierOptionID = "service_tier"
)

// codexTierCapture is one app-server request observed by the fake. It is the
// cross-process evidence surface asserted by the integration tests.
type codexTierCapture struct {
	Method         string `json:"method"`
	ThreadID       string `json:"thread_id,omitempty"`
	Model          string `json:"model,omitempty"`
	ServiceTier    string `json:"service_tier,omitempty"`
	ServiceTierSet bool   `json:"service_tier_set,omitempty"`
	Rejected       bool   `json:"rejected,omitempty"`
}

// codexTierCaptureLog appends one JSON record per captured request. The helper
// opens it eagerly so a missing or unwritable evidence file fails the child
// instead of silently weakening parent assertions.
type codexTierCaptureLog struct {
	file *os.File
}

func newCodexTierCaptureLog(path string) (*codexTierCaptureLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &codexTierCaptureLog{file: file}, nil
}

func (l *codexTierCaptureLog) append(record codexTierCapture) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = l.file.Write(append(encoded, '\n'))
	return err
}

// codexTierHelperConfig is the child-side script passed by the parent test.
type codexTierHelperConfig struct {
	CapturePath   string
	WorkspaceRoot string
	// ResumeModel and ResumeTier describe the persisted remote Thread state a
	// resumed activation must observe. An empty ResumeTier means the remote
	// Thread carries no explicit tier.
	ResumeModel string
	ResumeTier  string
	// RejectFirstTurnAfterResume makes the first turn/start of the whole test
	// run that follows a thread/resume fail with an explicit JSON-RPC error.
	RejectFirstTurnAfterResume bool
}

// TestCodexServiceTierAdapterHelperProcess is the re-executed child endpoint for
// the Codex service-tier integration tests. It is inert in ordinary runs.
func TestCodexServiceTierAdapterHelperProcess(t *testing.T) {
	args := os.Args
	markerIndex := -1
	for index, arg := range args {
		if arg == codexTierHelperMarker {
			markerIndex = index
			break
		}
	}
	if markerIndex < 0 {
		return
	}
	if markerIndex+5 >= len(args) {
		fmt.Fprintln(os.Stderr, "codex tier helper: incomplete arguments")
		os.Exit(2)
	}
	config := codexTierHelperConfig{
		CapturePath:                args[markerIndex+1],
		WorkspaceRoot:              args[markerIndex+2],
		ResumeModel:                args[markerIndex+3],
		ResumeTier:                 args[markerIndex+4],
		RejectFirstTurnAfterResume: args[markerIndex+5] == "1",
	}
	if err := serveCodexTierAdapter(config); err != nil {
		fmt.Fprintln(os.Stderr, "codex tier helper:", err)
		os.Exit(3)
	}
	os.Exit(0)
}

func serveCodexTierAdapter(config codexTierHelperConfig) error {
	ctx := context.Background()
	log, err := newCodexTierCaptureLog(config.CapturePath)
	if err != nil {
		return fmt.Errorf("open capture log: %w", err)
	}
	defer log.file.Close()
	// One pipe per direction: the adapter writes requests to the fake, and the
	// fake writes responses and notifications back to the adapter.
	backendIn, fakeOut := io.Pipe()
	fakeIn, backendOut := io.Pipe()
	fake := &codexTierAppServer{
		config:  config,
		log:     log,
		threads: map[string]*codexTierThread{},
		reader:  bufio.NewReader(fakeIn), writer: fakeOut,
	}
	go func() {
		if err := fake.serve(); err != nil {
			fmt.Fprintln(os.Stderr, "codex tier app-server:", err)
			os.Exit(4)
		}
	}()
	backend, err := codex.NewBackend(ctx, backendIn, backendOut)
	if err != nil {
		return err
	}
	defer backend.Close()
	return backend.ServeACP(ctx, codex.ConnectionOptions{
		ConnectionID: "codex",
		Workspace: codex.WorkspacePolicy{
			AllowedRoots: []string{config.WorkspaceRoot}, WritableRoots: []string{config.WorkspaceRoot},
		},
	}, os.Stdin, os.Stdout)
}

type codexTierThread struct {
	cwd   string
	model string
	// tier is nil while the Thread has no explicit remote tier.
	tier *string
}

type codexTierRPCError struct {
	code    int
	message string
}

type codexTierNotification struct {
	method string
	params any
}

// codexTierAppServer is the fake Codex app-server. It speaks the app-server's
// line-delimited JSON subset (no jsonrpc version member) and never invents
// state: thread/start reports no explicit tier, and thread/resume reports only
// what the scripted remote Thread persisted. Requests arrive sequentially on
// one reader, so its own state needs no locking.
type codexTierAppServer struct {
	config codexTierHelperConfig
	log    *codexTierCaptureLog
	reader *bufio.Reader
	writer io.Writer

	threads  map[string]*codexTierThread
	opened   int
	turns    int
	resumed  bool
	rejected bool
}

func (s *codexTierAppServer) serve() error {
	for {
		line, err := s.reader.ReadBytes('\n')
		if len(line) > 0 {
			var request struct {
				ID     json.RawMessage            `json:"id"`
				Method string                     `json:"method"`
				Params map[string]json.RawMessage `json:"params"`
			}
			if json.Unmarshal(line, &request) == nil && request.Method != "" {
				if err := s.dispatch(request.ID, request.Method, request.Params); err != nil {
					return err
				}
			}
		}
		if err != nil {
			return nil
		}
	}
}

func (s *codexTierAppServer) dispatch(id json.RawMessage, method string, params map[string]json.RawMessage) error {
	result, rpcErr, notify, err := s.handle(method, params)
	if err != nil {
		return err
	}
	if rpcErr != nil {
		return s.write(map[string]any{"id": id, "error": map[string]any{"code": rpcErr.code, "message": rpcErr.message}})
	}
	if err := s.write(map[string]any{"id": id, "result": result}); err != nil {
		return err
	}
	if notify != nil {
		return s.write(map[string]any{"method": notify.method, "params": notify.params})
	}
	return nil
}

func (s *codexTierAppServer) write(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.writer.Write(append(encoded, '\n'))
	return err
}

func (s *codexTierAppServer) handle(method string, params map[string]json.RawMessage) (any, *codexTierRPCError, *codexTierNotification, error) {
	switch method {
	case "initialize":
		return map[string]any{"codexHome": s.config.WorkspaceRoot}, nil, nil, nil
	case "account/read":
		return map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}, nil, nil, nil
	case "thread/start":
		return s.threadStart(params)
	case "thread/read":
		return s.threadRead(params)
	case "thread/resume":
		return s.threadResume(params)
	case "model/list":
		return map[string]any{"data": codexTierCatalog()}, nil, nil, nil
	case "turn/start":
		return s.turnStart(params)
	case "turn/interrupt":
		return map[string]any{}, nil, nil, nil
	case "thread/unsubscribe":
		return map[string]any{"status": "unsubscribed"}, nil, nil, nil
	default:
		return nil, &codexTierRPCError{code: -32601, message: "unknown method " + method}, nil, nil
	}
}

func (s *codexTierAppServer) threadStart(params map[string]json.RawMessage) (any, *codexTierRPCError, *codexTierNotification, error) {
	cwd := rawString(params["cwd"])
	s.opened++
	threadID := fmt.Sprintf("thread-%d", s.opened)
	s.threads[threadID] = &codexTierThread{cwd: cwd, model: codexTierTieredID}
	if err := s.log.append(codexTierCapture{Method: "thread/start", ThreadID: threadID}); err != nil {
		return nil, nil, nil, err
	}
	// A null serviceTier is the real app-server behavior for an unconfigured
	// Thread: the adapter must not treat the catalog default as staged.
	return map[string]any{
		"thread": map[string]any{"id": threadID, "cwd": cwd},
		"model":  codexTierTieredID, "serviceTier": nil,
	}, nil, nil, nil
}

func (s *codexTierAppServer) threadRead(params map[string]json.RawMessage) (any, *codexTierRPCError, *codexTierNotification, error) {
	threadID := rawString(params["threadId"])
	// The remote Thread of a resumed activation belongs to an earlier adapter
	// process; the real app-server still records its workspace CWD.
	cwd := s.config.WorkspaceRoot
	if thread := s.threads[threadID]; thread != nil {
		cwd = thread.cwd
	}
	return map[string]any{"thread": map[string]any{"id": threadID, "cwd": cwd}}, nil, nil, nil
}

func (s *codexTierAppServer) threadResume(params map[string]json.RawMessage) (any, *codexTierRPCError, *codexTierNotification, error) {
	threadID := rawString(params["threadId"])
	thread := s.threads[threadID]
	if thread == nil {
		thread = &codexTierThread{cwd: firstNonEmptyString(rawString(params["cwd"]), s.config.WorkspaceRoot), model: s.config.ResumeModel}
		if strings.TrimSpace(s.config.ResumeTier) != "" {
			tier := s.config.ResumeTier
			thread.tier = &tier
		}
		s.threads[threadID] = thread
	}
	s.resumed = true
	model, tier := thread.model, thread.tier
	capture := codexTierCapture{Method: "thread/resume", ThreadID: threadID, Model: model}
	if tier != nil {
		capture.ServiceTier, capture.ServiceTierSet = *tier, true
	}
	if err := s.log.append(capture); err != nil {
		return nil, nil, nil, err
	}
	result := map[string]any{
		"thread": map[string]any{"id": threadID, "cwd": thread.cwd}, "model": model,
	}
	if tier != nil {
		result["serviceTier"] = *tier
	} else {
		result["serviceTier"] = nil
	}
	return result, nil, nil, nil
}

func (s *codexTierAppServer) turnStart(params map[string]json.RawMessage) (any, *codexTierRPCError, *codexTierNotification, error) {
	threadID := rawString(params["threadId"])
	model := rawString(params["model"])
	var tier *string
	if rawTier, ok := params["serviceTier"]; ok && string(rawTier) != "null" {
		value := rawString(rawTier)
		tier = &value
	}
	s.turns++
	turnID := fmt.Sprintf("turn-%d", s.turns)
	reject := s.config.RejectFirstTurnAfterResume && s.resumed && !s.rejected && claimCodexTierRejection(s.config.CapturePath)
	if reject {
		s.rejected = true
	} else if thread := s.threads[threadID]; thread != nil {
		if model != "" {
			thread.model = model
		}
		thread.tier = tier
	}
	capture := codexTierCapture{Method: "turn/start", ThreadID: threadID, Model: model, Rejected: reject}
	if tier != nil {
		capture.ServiceTier, capture.ServiceTierSet = *tier, true
	}
	if err := s.log.append(capture); err != nil {
		return nil, nil, nil, err
	}
	if reject {
		return nil, &codexTierRPCError{code: -32602, message: "service tier unavailable for this model"}, nil, nil
	}
	return map[string]any{"turn": map[string]any{"id": turnID}}, nil, &codexTierNotification{
		method: "turn/completed",
		params: map[string]any{
			"threadId": threadID,
			"turn":     map[string]any{"id": turnID, "status": "completed"},
		},
	}, nil
}

func rawString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// claimCodexTierRejection elects exactly one process across the whole test run.
func claimCodexTierRejection(capturePath string) bool {
	file, err := os.OpenFile(capturePath+".rejected", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// codexTierCatalog is the fake app-server model catalog: one model whose
// catalog default is Fast, one model that advertises no additional tiers, and
// one whose configured profile declares no speed selection.
func codexTierCatalog() []map[string]any {
	tiered := []map[string]any{
		{"id": codexTierPriority, "name": "Fast"},
		{"id": codexTierStandard, "name": "Standard"},
	}
	return []map[string]any{
		{
			"id": codexTierTieredID, "model": codexTierTieredID, "displayName": "Codex Fast default",
			"defaultServiceTier": codexTierPriority, "serviceTiers": tiered,
		},
		{"id": codexTierTierlessID, "model": codexTierTierlessID, "displayName": "Codex tierless"},
		{
			"id": codexTierUnconfiguredID, "model": codexTierUnconfiguredID, "displayName": "Codex unconfigured",
			"defaultServiceTier": codexTierPriority, "serviceTiers": tiered,
		},
	}
}
