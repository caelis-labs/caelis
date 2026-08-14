package displaymodel

import "strings"

type ToolEvent struct {
	Name   string
	Args   string
	Output string
	Done   bool
	Err    bool
}

type ToolEventViewModel struct {
	Name       string
	Args       string
	Output     string
	Done       bool
	Err        bool
	Expandable bool
	Expanded   bool
	ClickToken string
}

func BuildToolEventViewModel(ev ToolEvent) ToolEventViewModel {
	return ToolEventViewModel{
		Name:   strings.TrimSpace(ev.Name),
		Args:   strings.TrimSpace(ev.Args),
		Output: strings.TrimSpace(ev.Output),
		Done:   ev.Done,
		Err:    ev.Err,
	}
}

func RenderToolEventLine(vm ToolEventViewModel) string {
	name := ToolEventDisplayName(vm.Name)
	if !vm.Done {
		prefix := "▸"
		if vm.Expandable && vm.Expanded {
			prefix = "▾"
		}
		line := prefix + " " + name
		if vm.Args != "" {
			line += " " + vm.Args
		}
		return line
	}
	if vm.Err {
		line := "✗ " + name
		if vm.Output != "" {
			line += " " + vm.Output
		}
		return line
	}
	line := "✓ " + name
	if vm.Output != "" {
		line += " " + vm.Output
	} else {
		line += " completed"
	}
	return line
}

func ToolEventDisplayName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "TOOL"
	}
	switch strings.ToLower(name) {
	case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "other":
		return strings.ToUpper(name[:1]) + name[1:]
	}
	switch strings.ToUpper(strings.ReplaceAll(name, " ", "_")) {
	case "THINK":
		return "Think"
	default:
		if name == "" {
			return "TOOL"
		}
		return name
	}
}
