// Package codemode defines the controlled script boundary. Runtime supplies
// the only authoritative Host; the language worker receives bounded JSON only.
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

// ToolName is the fixed model-facing script entry.
const ToolName = "js_exec"

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
// JSON object. Returned media is selected by Runtime from the final script value.
// Cancellation of a supplied context may shorten, never extend, Runtime scope.
type Host interface {
	Call(context.Context, string, json.RawMessage) (Reply, error)
}

// SearchRequest selects either a name/capability query or an exact MCP server.
// Plugin optionally qualifies Server; it is invalid without Server.
type SearchRequest struct {
	Query  string `json:"query,omitempty"`
	Server string `json:"server,omitempty"`
	Plugin string `json:"plugin,omitempty"`
}

// DiscoveryHost adds the existing scoped discovery owner to the bridge.
// Search returns full schemas and records discovery through the existing owner.
type DiscoveryHost interface {
	Host
	Search(context.Context, SearchRequest) (Reply, error)
}

// Output separates ordinary script JSON from adapter-authenticated returned parts.
// Reply is true only for a real bridge reply returned directly. Otherwise Value
// is ordinary JSON and Parts may contain only selected opaque media references.
// These fields are internal executor metadata, never markers in business JSON.
type Output struct {
	Value json.RawMessage `json:"value,omitempty"`
	Parts []Part          `json:"parts,omitempty"`
	Reply bool            `json:"reply,omitempty"`
}

// Executor is the trusted Go adapter for one engine invocation. The adapter must
// translate Host into bounded JSON RPC; it must never expose Host or other Go
// objects to the untrusted language engine. Implementations bound CPU and report
// their memory enforcement limits, stop on cancellation, exclude idle Host
// waits, and enforce Limits before RPC allocation/encoding. The engine receives
// only JSON data and
// opaque handles, never Session, tool registry, credentials or filesystem access.
type Executor interface {
	Execute(context.Context, Source, Limits, Host) (Output, error)
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

// DefaultLimits returns conservative per-invocation transfer limits.
func DefaultLimits() Limits {
	return Limits{SourceBytes: 256 << 10, ArgumentBytes: 64 << 10, ReplyBytes: 1 << 20, OutputBytes: 1 << 20, RetainedBytes: 16 << 20, Calls: 64}
}

// MetadataSelectedTools is an optional AgentSpec.Metadata []string of canonical
// tool names selected by the embedder for the bounded Session hint snapshot.
// It is presentation policy, never discovery or authorization.
const MetadataSelectedTools = "code_mode_selected_tools"

// MetadataCatalog carries the Runtime-owned guidance and partial hint snapshot
// through both static AgentSpec metadata and dynamic request instructions.
const MetadataCatalog = "code_mode_catalog_v3"

// Guidance is the fixed model-facing SDK contract. Runtime appends its partial
// catalog hints separately so discovery never changes this instruction slice.
// MetadataScriptCompletion records whether script execution returned normally.
// Its "completed"/"failed" value is presentation-only, independent of cumulative
// IsError, Task state, authorization and durable effect outcomes.
const MetadataScriptCompletion = "code_mode_script_completion"

const Guidance = `Code Mode SDK: await tools.search(query) matches an exact tool name first, otherwise searches capabilities. tools.search({server,plugin?}) lists tools of an exact MCP server without fuzzy selection. Both return {tools:[{name,description,input_schema}],count,truncated,omitted_count?} with complete schemas; reuse valid bindings. await tools.call(name,args) returns {parts:[{kind,text?,json?,handle?,mime_type?}]}. Follow the schema and tool descriptions; choose sequential or concurrent calls according to their semantics. tools.unpack(reply) returns {json:[],text:[],media:[]} retaining every part and original media references. Explicitly return desired JSON or original reply/part references to deliver output and media; copying media fields does not preserve identity. Return only JSON values: omit undefined fields or use null; no return yields null. Numbers use JS Number precision; use strings for big integers and reply.raw_json for original JSON tokens. Host failures reject with error.reply containing the original projected reply and optional error.code for typed Host errors; local errors may lack reply. Catching does not erase failed records. Never blindly replay unknown outcomes. After a stale binding error, use tools.search(name) to reload its schema/binding. Native filesystem tools remain directly callable; shell execution stays outside js_exec.`

// Tool is a concrete declaration, retained until Runtime injects its Host.
// Product assembly uses the fixed ToolName. Every execution has a new VM.
type Tool struct {
	Name     string
	Executor Executor
	Sandbox  sandbox.Runtime
	Limits   Limits
}

// Definition declares mutually exclusive inline and file source inputs.
func (t *Tool) Definition() tool.Definition {
	return tool.Definition{Name: t.Name, Metadata: map[string]any{tool.MetadataToolKind: ToolKind}, Description: "Execute an async JavaScript function body from code or file in fresh state. Use await for tools.search(query) and tools.call(name,args), then explicitly return the desired output. No Node.js, modules, direct filesystem, network or process access. Shell execution stays outside js_exec. Long executions may yield a Task handle.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"code": map[string]any{"type": "string", "minLength": 1, "description": "Async JavaScript function body; await tool calls and explicitly return the desired output."}, "file": map[string]any{"type": "string", "minLength": 1, "description": "JavaScript source file path; relative paths resolve from the working directory."}, "yield_time_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 60000, "default": 10000, "description": "Foreground wait only, in milliseconds. After yielding, use Task read/wait/cancel on the original handle; do not resubmit js_exec."}},
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
		Code  json.RawMessage `json:"code"`
		File  json.RawMessage `json:"file"`
		Yield *int            `json:"yield_time_ms"`
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
