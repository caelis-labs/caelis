// Package codemode defines an experimental, engine-neutral script boundary.
// It does not implement a language, expose Go objects to scripts, or register a
// product tool. Runtime supplies the only authoritative Host implementation.
package codemode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ToolKind identifies declarations that require Runtime Code Mode binding.
const ToolKind = "code_mode"

// Outcome values project existing execution receipts without granting retry authority.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeUnknown   = "unknown_outcome"
)

// Source is one immutable read. Adapters execute Code, never reopen Path.
type Source struct {
	Code   string `json:"code"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256"`
}

// Part is the script-visible projection of a tool part. Media and other opaque
// content have only a handle; paths, credentials and media bytes stay in Go.
type Part struct {
	Kind     string          `json:"kind"`
	Text     string          `json:"text,omitempty"`
	JSON     json.RawMessage `json:"json,omitempty"`
	Handle   string          `json:"handle,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
}

// Reply retains tool disposition independently of its projected content.
type Reply struct {
	CallID       string `json:"call_id"`
	Outcome      string `json:"outcome"`
	Parts        []Part `json:"parts,omitempty"`
	IsError      bool   `json:"is_error,omitempty"`
	TurnComplete bool   `json:"turn_complete,omitempty"`
}

// Host is the executor's complete effect capability. Call arguments must be a
// JSON object. Emit selects original opaque content for the final model result.
// Cancellation of a supplied context may shorten, never extend, Runtime scope.
type Host interface {
	Call(context.Context, string, json.RawMessage) (Reply, error)
	Emit(context.Context, string) error
}

// Executor is the trusted Go adapter for one engine invocation. The adapter must
// translate Host into bounded JSON RPC; it must never expose Host or other Go
// objects to the untrusted language engine. Implementations bound CPU/memory,
// stop on cancellation, pause CPU accounting during Host waits, and enforce
// Limits before RPC allocation/encoding. The engine receives only JSON data and
// opaque handles, never Session, tool registry, credentials or filesystem access.
type Executor interface {
	Execute(context.Context, Source, Limits, Host) (json.RawMessage, error)
}

// Limits bound allocations/transfers at the common boundary. Tool-internal
// allocations remain the owning tool's responsibility. Zero selects defaults.
type Limits struct {
	SourceBytes   int `json:"source_bytes"`
	ArgumentBytes int `json:"argument_bytes"`
	ReplyBytes    int `json:"reply_bytes"`
	// OutputBytes bounds executor JSON. Opaque media emitted to the model is
	// bounded by the invocation's cumulative RetainedBytes and Calls limits.
	OutputBytes   int `json:"output_bytes"`
	RetainedBytes int `json:"retained_bytes"`
	Calls         int `json:"calls"`
}

// DefaultLimits returns conservative per-invocation PoC limits.
func DefaultLimits() Limits {
	return Limits{SourceBytes: 256 << 10, ArgumentBytes: 64 << 10, ReplyBytes: 1 << 20, OutputBytes: 1 << 20, RetainedBytes: 16 << 20, Calls: 64}
}

// Tool is a concrete declaration, retained until Runtime injects its Host.
// JavaScript adapters use js_repl; node_repl is reserved for actual Node.
type Tool struct {
	Name     string
	Executor Executor
	Sandbox  sandbox.Runtime
	Limits   Limits
}

// Definition declares mutually exclusive inline and file source inputs.
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{Name: t.Name, Metadata: map[string]any{tool.MetadataToolKind: ToolKind}, Description: "Execute code using the controlled Runtime host bridge. Supply exactly one of code or file. File bytes are read anew for every invocation.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"code": map[string]any{"type": "string", "minLength": 1}, "file": map[string]any{"type": "string", "minLength": 1}},
		"oneOf":      []any{map[string]any{"required": []string{"code"}}, map[string]any{"required": []string{"file"}}},
	}}
}

// Call fails closed outside Runtime; a declaration never grants a raw bridge.
func (*Tool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, tool.NewError(tool.ErrorCodeInvalidInput, "Code Mode requires Runtime host binding")
}

// EffectiveLimits validates and fills the boundary limits.
func (t *Tool) EffectiveLimits() (Limits, error) {
	l, d := t.Limits, DefaultLimits()
	for _, pair := range [][2]*int{{&l.SourceBytes, &d.SourceBytes}, {&l.ArgumentBytes, &d.ArgumentBytes}, {&l.ReplyBytes, &d.ReplyBytes}, {&l.OutputBytes, &d.OutputBytes}, {&l.RetainedBytes, &d.RetainedBytes}, {&l.Calls, &d.Calls}} {
		if *pair[0] < 0 {
			return Limits{}, fmt.Errorf("code mode limits must be positive")
		}
		if *pair[0] == 0 {
			*pair[0] = *pair[1]
		}
	}
	return l, nil
}

// ReadSource freezes one bounded input using the supplied sandbox filesystem,
// including policy-selected filesystem constraints from Runtime admission.
func (t *Tool) ReadSource(call tool.Call, limit int) (Source, error) {
	if len(call.Input) > limit*6+1024 { // JSON escaping can expand one source byte.
		return Source{}, fmt.Errorf("code mode source input exceeds budget")
	}
	var input struct {
		Code json.RawMessage `json:"code"`
		File json.RawMessage `json:"file"`
	}
	dec := json.NewDecoder(strings.NewReader(string(call.Input)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return Source{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Source{}, fmt.Errorf("code mode requires exactly one source object")
	}
	if (len(input.Code) == 0) == (len(input.File) == 0) {
		return Source{}, fmt.Errorf("supply exactly one of code and file")
	}
	source := Source{}
	if len(input.Code) > 0 {
		if err := json.Unmarshal(input.Code, &source.Code); err != nil {
			return Source{}, err
		}
	} else {
		var filePath string
		if err := json.Unmarshal(input.File, &filePath); err != nil {
			return Source{}, err
		}
		if t.Sandbox == nil || filePath == "" {
			return Source{}, fmt.Errorf("code mode file requires a sandbox filesystem and path")
		}
		fsys := t.Sandbox.FileSystem()
		if constrained, ok := t.Sandbox.(interface {
			FileSystemFor(sandbox.Constraints) sandbox.FileSystem
		}); ok {
			var constraints sandbox.Constraints
			if raw, ok := call.Metadata["sandbox_constraints"]; ok {
				encoded, err := json.Marshal(raw)
				if err != nil {
					return Source{}, err
				}
				if err := json.Unmarshal(encoded, &constraints); err != nil {
					return Source{}, err
				}
			}
			fsys = constrained.FileSystemFor(sandbox.NormalizeConstraints(constraints))
		}
		if fsys == nil {
			return Source{}, fmt.Errorf("code mode filesystem is unavailable")
		}
		path := filePath
		if strings.HasPrefix(path, "~/") {
			home, err := fsys.UserHomeDir()
			if err != nil {
				return Source{}, err
			}
			path = filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) {
			cwd, err := fsys.Getwd()
			if err != nil {
				return Source{}, err
			}
			path = filepath.Join(cwd, path)
		}
		source.Path = filepath.Clean(path)
		file, err := fsys.Open(source.Path)
		if err != nil {
			return Source{}, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return Source{}, err
		}
		if !info.Mode().IsRegular() || info.Size() > int64(limit) {
			return Source{}, fmt.Errorf("code mode source must be a bounded regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
		if err != nil {
			return Source{}, err
		}
		source.Code = string(data)
	}
	if len(source.Code) == 0 || len(source.Code) > limit || !utf8.ValidString(source.Code) {
		return Source{}, fmt.Errorf("code mode source must be nonempty bounded UTF-8")
	}
	sum := sha256.Sum256([]byte(source.Code))
	source.SHA256 = hex.EncodeToString(sum[:])
	return source, nil
}
