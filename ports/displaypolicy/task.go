package displaypolicy

import "strings"

func ToolTaskID(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_id"),
		MapString(output, "handle"),
		MapString(output, "task_id"),
		MapString(input, "task_id"),
	)
}

func ToolTaskAction(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "action"),
		MapString(output, "action"),
		MapString(input, "action"),
	))
}

func ToolTaskInput(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "input"),
		MapString(output, "input"),
		MapString(input, "input"),
	)
}

func ToolTaskTargetKind(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_kind"),
		MapString(output, "target_kind"),
		MapString(output, "kind"),
		MapString(input, "target_kind"),
	))
}
