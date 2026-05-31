package tool

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

const (
	RuntimeTaskMetaName    = "caelis.runtime.task"
	RuntimeTaskMetaVersion = 1
)

// WithRuntimeTaskMeta returns meta with task installed under the canonical
// caelis.runtime.task namespace. The caller retains ownership of task.
func WithRuntimeTaskMeta(meta map[string]any, task map[string]any) map[string]any {
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis := cloneNestedMap(out["caelis"])
	runtimeMeta := cloneNestedMap(caelis["runtime"])
	taskMeta := maps.Clone(task)
	if taskMeta == nil {
		taskMeta = map[string]any{}
	}
	taskMeta["schema"] = RuntimeTaskMetaName
	taskMeta["schema_version"] = RuntimeTaskMetaVersion
	runtimeMeta["task"] = taskMeta
	caelis["runtime"] = runtimeMeta
	if _, ok := caelis["version"]; !ok {
		caelis["version"] = RuntimeTaskMetaVersion
	}
	out["caelis"] = caelis
	return out
}

// RuntimeTaskMeta returns the canonical runtime task metadata section.
func RuntimeTaskMeta(meta map[string]any) map[string]any {
	task, ok := mapAny(nestedAny(meta, "caelis", "runtime", "task"))
	if !ok || len(task) == 0 {
		return nil
	}
	return task
}

// RuntimeTaskValue returns one value from the canonical runtime task metadata.
func RuntimeTaskValue(meta map[string]any, key string) any {
	task := RuntimeTaskMeta(meta)
	if len(task) == 0 {
		return nil
	}
	return task[strings.TrimSpace(key)]
}

// RuntimeTaskOutputText returns the best display-safe task output preview.
func RuntimeTaskOutputText(meta map[string]any) string {
	task := RuntimeTaskMeta(meta)
	if len(task) == 0 {
		return ""
	}
	for _, key := range []string{"output_text", "latest_output", "output_preview", "result", "output", "final_message", "finalMessage", "text", "error"} {
		if text := anyText(task[key]); text != "" {
			return text
		}
	}
	return JoinRuntimeTaskStreams(anyText(task["stdout"]), anyText(task["stderr"]))
}

// RuntimeTaskPreview builds the common preview fields for terminal-backed
// runtime tasks.
func RuntimeTaskPreview(stdout string, stderr string, stdoutDropped int64, stderrDropped int64, stdoutCursor int64, stderrCursor int64) map[string]any {
	out := map[string]any{}
	if stdout != "" {
		out["stdout_preview"] = stdout
	}
	if stderr != "" {
		out["stderr_preview"] = stderr
	}
	if text := JoinRuntimeTaskStreams(stdout, stderr); text != "" {
		out["output_preview"] = text
	}
	if stdoutDropped > 0 {
		out["stdout_dropped_bytes"] = stdoutDropped
	}
	if stderrDropped > 0 {
		out["stderr_dropped_bytes"] = stderrDropped
	}
	if stdoutDropped > 0 || stderrDropped > 0 {
		out["output_truncated"] = true
	}
	if stdoutCursor > 0 {
		out["stdout_cursor"] = stdoutCursor
	}
	if stderrCursor > 0 {
		out["stderr_cursor"] = stderrCursor
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// JoinRuntimeTaskStreams combines stdout and stderr without losing stream text.
func JoinRuntimeTaskStreams(stdout string, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	case strings.HasSuffix(stdout, "\n") || strings.HasPrefix(stderr, "\n"):
		return stdout + stderr
	default:
		return stdout + "\n" + stderr
	}
}

func cloneNestedMap(value any) map[string]any {
	out, ok := mapAny(value)
	if !ok {
		return map[string]any{}
	}
	return maps.Clone(out)
}

func mapAny(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	default:
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			return nil, false
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil || len(out) == 0 {
			return nil, false
		}
		return out, true
	}
}

func nestedAny(values map[string]any, path ...string) any {
	if len(values) == 0 {
		return nil
	}
	var current any = values
	for _, key := range path {
		mapped, ok := mapAny(current)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func anyText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}
