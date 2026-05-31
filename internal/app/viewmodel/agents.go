package viewmodel

import "maps"

type AgentManagementView struct {
	Registered        []AgentManagementItem   `json:"registered,omitempty"`
	Builtins          []AgentManagementItem   `json:"builtins,omitempty"`
	Installable       []AgentInstallItem      `json:"installable,omitempty"`
	CanRegisterCustom bool                    `json:"can_register_custom,omitempty"`
	Actions           []AgentManagementAction `json:"actions,omitempty"`
}

type AgentManagementItem struct {
	Agent       AgentItem               `json:"agent"`
	Source      string                  `json:"source,omitempty"`
	Registered  bool                    `json:"registered,omitempty"`
	Builtin     bool                    `json:"builtin,omitempty"`
	Installable bool                    `json:"installable,omitempty"`
	Actions     []AgentManagementAction `json:"actions,omitempty"`
}

type AgentInstallItem struct {
	ID      string                  `json:"id,omitempty"`
	Name    string                  `json:"name,omitempty"`
	Detail  string                  `json:"detail,omitempty"`
	Actions []AgentManagementAction `json:"actions,omitempty"`
}

type AgentManagementAction struct {
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	Command       string `json:"command,omitempty"`
	Enabled       bool   `json:"enabled,omitempty"`
	Destructive   bool   `json:"destructive,omitempty"`
	RequiresInput bool   `json:"requires_input,omitempty"`
}

func CloneAgentManagementView(in AgentManagementView) AgentManagementView {
	out := in
	out.Registered = cloneAgentManagementItems(in.Registered)
	out.Builtins = cloneAgentManagementItems(in.Builtins)
	out.Installable = cloneAgentInstallItems(in.Installable)
	out.Actions = append([]AgentManagementAction(nil), in.Actions...)
	return out
}

func cloneAgentManagementItems(in []AgentManagementItem) []AgentManagementItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]AgentManagementItem, 0, len(in))
	for _, item := range in {
		item.Agent.Args = append([]string(nil), item.Agent.Args...)
		item.Agent.Meta = maps.Clone(item.Agent.Meta)
		item.Actions = append([]AgentManagementAction(nil), item.Actions...)
		out = append(out, item)
	}
	return out
}

func cloneAgentInstallItems(in []AgentInstallItem) []AgentInstallItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]AgentInstallItem, 0, len(in))
	for _, item := range in {
		item.Actions = append([]AgentManagementAction(nil), item.Actions...)
		out = append(out, item)
	}
	return out
}
