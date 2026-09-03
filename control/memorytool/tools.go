package memorytool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/memorybinding"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	memorysdk "github.com/caelis-labs/memory/sdk/go/memory"
)

const (
	RememberToolName = "Remember"
	RecallToolName   = "Recall"

	DefaultRecallFragments       = 8
	DefaultRecallProjectionBytes = 8 << 10
	DefaultRecallDeadlineMS      = 3_000
	metadataVersion              = 1
	emptyRecallMessage           = "No matching memories found."
	incompleteRecallMessage      = "Memory recall was incomplete; no fragments were returned."
)

// Client is the pure bound Memory SDK behavior needed by the tool adapter.
type Client interface {
	Remember(context.Context, string, string, *time.Time) (v1alpha1.RememberResponse, error)
	Recall(context.Context, string, v1alpha1.ConsistencyToken) (v1alpha1.RecallResponse, error)
}

// Config fixes all hidden tool authority and persistence dependencies for one
// canonical Session Runtime.
type Config struct {
	Client             Client
	Sessions           session.StateStore
	SessionRef         session.SessionRef
	Binding            memorybinding.RuntimeMemoryBindingSnapshot
	MaxProjectionBytes int
}

// New returns exactly Remember and Recall, in that order.
func New(config Config) ([]tool.Tool, error) {
	if config.Client == nil || config.Sessions == nil || strings.TrimSpace(config.SessionRef.SessionID) == "" {
		return nil, fmt.Errorf("control/memorytool: client, Session state, and Session reference are required")
	}
	if config.MaxProjectionBytes <= 0 {
		config.MaxProjectionBytes = DefaultRecallProjectionBytes
	}
	base := baseTool{config: config}
	return []tool.Tool{
		&rememberTool{baseTool: base},
		&recallTool{baseTool: base},
	}, nil
}

// DefaultRecallBudget is the host-selected Alpha bound hidden from tool input.
func DefaultRecallBudget() v1alpha1.RecallBudget {
	return v1alpha1.RecallBudget{
		MaxFragments: DefaultRecallFragments,
		MaxBytes:     DefaultRecallProjectionBytes,
		DeadlineMS:   DefaultRecallDeadlineMS,
	}
}

type baseTool struct {
	config Config
}

type rememberTool struct{ baseTool }
type recallTool struct{ baseTool }

func (*rememberTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RememberToolName,
		Description: "Persist established facts, decisions, preferences, commitments, or corrections that are likely to be useful in later interactions. Exclude transient status, intermediate work, speculation, logs, and secrets.",
		InputSchema: singleStringSchema(
			"text",
			"One concise, self-contained statement containing one durable item. Identify the subject, relation or decision, exact value, and relevant scope or time. Include exact names and known literal aliases. For a correction, state both the superseded and current values.",
		),
		EffectClass: tool.EffectIdempotent,
	}
}

func (*recallTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        RecallToolName,
		Description: "Retrieve durable memory when prior facts, decisions, preferences, commitments, or corrections may affect the current task or answer.",
		InputSchema: singleStringSchema(
			"query",
			"A short literal keyword string. Put the most distinctive entity or name first, followed by discriminating attributes, exact values, and known abbreviations or Chinese/English aliases. Matching is lexical and OR-based; Boolean operators and regular expressions are not supported. Avoid full natural-language questions and semantic paraphrases.",
		),
		EffectClass: tool.EffectReadOnly,
	}
}

func (t *rememberTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.remember(ctx, call)
}

func (t *rememberTool) Recover(ctx context.Context, request tool.RecoveryRequest) (tool.RecoveryResult, error) {
	result, err := t.remember(ctx, request.Call)
	if err == nil {
		return tool.RecoveryResult{Status: tool.RecoverySucceeded, Result: result}, nil
	}
	code, serviceError := v1alpha1.ErrorCodeOf(err)
	if !serviceError {
		var toolErr *tool.ToolError
		if errors.As(err, &toolErr) && toolErr.Code == tool.ErrorCodeInvalidInput {
			return tool.RecoveryResult{
				Status: tool.RecoveryFailed,
				Result: errorResult(request.Call, err),
				Reason: "the original Remember input was invalid",
			}, nil
		}
		return tool.RecoveryResult{Status: tool.RecoveryUnknown}, err
	}
	switch code {
	case v1alpha1.ErrorCodeUnavailable, v1alpha1.ErrorCodeDeadline,
		v1alpha1.ErrorCodeUnknownOutcome, v1alpha1.ErrorCodeInternal:
		return tool.RecoveryResult{Status: tool.RecoveryUnknown}, err
	default:
		return tool.RecoveryResult{
			Status: tool.RecoveryFailed,
			Result: errorResult(request.Call, mapMemoryError(err)),
			Reason: "Memory rejected the original Remember effect",
		}, nil
	}
}

func (t *rememberTool) remember(ctx context.Context, call tool.Call) (tool.Result, error) {
	input, err := decodeSingleString(call.Input, "text")
	if err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(call.ID) == "" {
		return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "Remember requires a stable tool call identity")
	}
	if _, err := memorybinding.PrepareConsistency(ctx, t.config.Sessions, t.config.SessionRef, t.config.Binding); err != nil {
		return tool.Result{}, fmt.Errorf("prepare Memory consistency: %w", err)
	}
	response, err := t.config.Client.Remember(ctx, input, rememberIdempotencyKey(t.config, call), nil)
	if err != nil {
		return tool.Result{}, mapMemoryError(err)
	}
	if !response.Accepted {
		return tool.Result{}, tool.NewError(tool.ErrorCode("internal"), "Memory did not acknowledge the fact")
	}
	if err := memorybinding.AdvanceConsistency(
		ctx,
		t.config.Sessions,
		t.config.SessionRef,
		t.config.Binding,
		string(response.ConsistencyToken),
	); err != nil {
		return tool.Result{}, fmt.Errorf("persist Memory consistency: %w", err)
	}
	projected, err := memorysdk.ProjectRemember(response)
	if err != nil {
		return tool.Result{}, err
	}
	return successResult(call, projected, rememberMetadata(t.config.Binding, response)), nil
}

func (t *recallTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	query, err := decodeSingleString(call.Input, "query")
	if err != nil {
		return tool.Result{}, err
	}
	token, err := memorybinding.PrepareConsistency(ctx, t.config.Sessions, t.config.SessionRef, t.config.Binding)
	if err != nil {
		return tool.Result{}, err
	}
	response, err := t.config.Client.Recall(ctx, query, v1alpha1.ConsistencyToken(token))
	if err != nil {
		return tool.Result{}, mapMemoryError(err)
	}
	if err := memorybinding.AdvanceConsistency(
		ctx,
		t.config.Sessions,
		t.config.SessionRef,
		t.config.Binding,
		string(response.ConsistencyToken),
	); err != nil {
		return tool.Result{}, fmt.Errorf("persist Memory consistency: %w", err)
	}
	projected, err := projectRecall(response, t.config.MaxProjectionBytes)
	if err != nil {
		if errors.Is(err, memorysdk.ErrProjectionBudgetExceeded) {
			return tool.Result{}, tool.NewError(tool.ErrorCodeOutputTruncated, "Memory recall exceeded the model-visible output bound")
		}
		return tool.Result{}, err
	}
	return successResult(call, projected, recallMetadata(t.config.Binding, response)), nil
}

func projectRecall(response v1alpha1.RecallResponse, maxBytes int) ([]byte, error) {
	projected, err := memorysdk.ProjectRecall(response, maxBytes)
	if err != nil || len(response.Fragments) != 0 {
		return projected, err
	}
	message := emptyRecallMessage
	if response.Degraded || response.Truncated {
		message = incompleteRecallMessage
	}
	projected, err = json.Marshal(struct {
		Fragments []string `json:"fragments"`
		Message   string   `json:"message"`
	}{
		Fragments: []string{},
		Message:   message,
	})
	if err != nil {
		return nil, err
	}
	if len(projected) > maxBytes {
		return nil, memorysdk.ErrProjectionBudgetExceeded
	}
	return projected, nil
}

func singleStringSchema(name, description string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{name: map[string]any{
			"type": "string", "description": description,
		}},
		"required":             []string{name},
		"additionalProperties": false,
	}
}

func decodeSingleString(raw json.RawMessage, name string) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	value := map[string]json.RawMessage{}
	if err := decoder.Decode(&value); err != nil {
		return "", tool.WrapError(tool.ErrorCodeInvalidInput, err, "invalid Memory tool input")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, "Memory tool input contains trailing data")
	}
	selected, found := value[name]
	if !found || len(value) != 1 {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, "only "+name+" is supported")
	}
	var text string
	if err := json.Unmarshal(selected, &text); err != nil {
		return "", tool.WrapError(tool.ErrorCodeInvalidInput, err, name+" must be a string")
	}
	if strings.TrimSpace(text) == "" {
		return "", tool.NewError(tool.ErrorCodeInvalidInput, name+" is required")
	}
	return text, nil
}

func rememberIdempotencyKey(config Config, call tool.Call) string {
	ref := session.NormalizeSessionRef(config.SessionRef)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"caelis-memory-remember-v1",
		string(config.Binding.RuntimeActorRef),
		ref.AppName,
		ref.UserID,
		ref.WorkspaceKey,
		ref.SessionID,
		strings.TrimSpace(call.ID),
	}, "\x00")))
	return "caelis:v1:" + hex.EncodeToString(sum[:])
}

func successResult(call tool.Call, projected []byte, metadata map[string]any) tool.Result {
	return tool.Result{
		ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name),
		Content:  []model.Part{model.NewJSONPart(projected)},
		Metadata: metadata,
	}
}

func errorResult(call tool.Call, err error) tool.Result {
	payload, _ := json.Marshal(tool.ErrorPayload(err))
	return tool.Result{
		ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name), IsError: true,
		Content: []model.Part{model.NewJSONPart(payload)},
	}
}

func mapMemoryError(err error) error {
	code, ok := v1alpha1.ErrorCodeOf(err)
	if !ok {
		return tool.WrapError(tool.ErrorCode("unavailable"), err, "Memory is unavailable")
	}
	mapped := &tool.ToolError{Code: tool.ErrorCode(code), Retryable: false, Err: err}
	switch code {
	case v1alpha1.ErrorCodeInvalidArgument:
		mapped.Code, mapped.Message = tool.ErrorCodeInvalidInput, "Memory rejected the request"
	case v1alpha1.ErrorCodeUnauthorized:
		mapped.Code, mapped.Message = tool.ErrorCodePermissionDenied, "Memory authorization was rejected"
	case v1alpha1.ErrorCodeNotFound:
		mapped.Code, mapped.Message = tool.ErrorCodeNotFound, "Memory data was not found"
	case v1alpha1.ErrorCodeDeadline:
		mapped.Code, mapped.Message, mapped.Retryable = tool.ErrorCodeTimeout, "Memory request timed out", true
	case v1alpha1.ErrorCodeUnavailable:
		mapped.Message, mapped.Retryable = "Memory is unavailable", true
	case v1alpha1.ErrorCodeUnknownOutcome:
		mapped.Message, mapped.Retryable = "Memory could not prove whether the operation completed", true
	case v1alpha1.ErrorCodeStaleConsistencyToken:
		mapped.Message = "Memory consistency state is stale"
	case v1alpha1.ErrorCodeIncompatible:
		mapped.Message = "Memory service is incompatible with this Caelis build"
	case v1alpha1.ErrorCodeConflict:
		mapped.Message = "Memory rejected a conflicting effect identity"
	default:
		mapped.Message = "Memory request failed"
	}
	return mapped
}

func baseMetadata(binding memorybinding.RuntimeMemoryBindingSnapshot, token v1alpha1.ConsistencyToken) map[string]any {
	memory := map[string]any{
		"version":         metadataVersion,
		"binding_ref":     string(binding.BindingRef),
		"view_ref":        binding.ViewRef,
		"audience":        string(binding.Audience),
		"binding_version": binding.BindingVersion,
	}
	if token != "" {
		memory["consistency_token"] = string(token)
	}
	return map[string]any{"caelis": map[string]any{
		"version": 1,
		"runtime": map[string]any{"memory": memory},
	}}
}

func rememberMetadata(binding memorybinding.RuntimeMemoryBindingSnapshot, response v1alpha1.RememberResponse) map[string]any {
	metadata := baseMetadata(binding, response.ConsistencyToken)
	memory := metadata["caelis"].(map[string]any)["runtime"].(map[string]any)["memory"].(map[string]any)
	memory["receipt_id"] = string(response.ReceiptID)
	memory["deduplicated_retry"] = response.DeduplicatedRetry
	memory["processing_state"] = string(response.ProcessingState)
	return metadata
}

func recallMetadata(binding memorybinding.RuntimeMemoryBindingSnapshot, response v1alpha1.RecallResponse) map[string]any {
	metadata := baseMetadata(binding, response.ConsistencyToken)
	memory := metadata["caelis"].(map[string]any)["runtime"].(map[string]any)["memory"].(map[string]any)
	fragments := make([]map[string]any, 0, len(response.Fragments))
	for _, fragment := range response.Fragments {
		evidence := make([]string, len(fragment.EvidenceRefs))
		for index, ref := range fragment.EvidenceRefs {
			evidence[index] = string(ref)
		}
		fragments = append(fragments, map[string]any{
			"fragment_id":   fragment.FragmentID,
			"evidence_refs": evidence,
			"record_refs":   append([]string(nil), fragment.RecordRefs...),
			"space_class":   string(fragment.SpaceClass),
		})
	}
	memory["fragments"] = fragments
	memory["degraded"] = response.Degraded
	memory["truncated"] = response.Truncated
	return metadata
}
