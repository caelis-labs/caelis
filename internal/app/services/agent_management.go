package services

import (
	"context"
	"maps"
	"sort"
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	agentActionRegisterCustom = "register_custom"
	agentActionInvoke         = "invoke"
	agentActionUseController  = "use_controller"
	agentActionRemove         = "remove"
	agentActionRegister       = "register"
	agentActionInstall        = "install"
	agentActionUpdate         = "update"
)

func (s AgentService) Management(ctx context.Context) (appviewmodel.AgentManagementView, error) {
	registered, err := s.List(ctx)
	if err != nil {
		return appviewmodel.AgentManagementView{}, err
	}
	builtins, err := s.ListBuiltins(ctx)
	if err != nil {
		return appviewmodel.AgentManagementView{}, err
	}
	installable, err := s.ListInstallableBuiltins(ctx)
	if err != nil {
		return appviewmodel.AgentManagementView{}, err
	}
	registeredKeys := agentRegisteredKeys(registered)
	view := appviewmodel.AgentManagementView{
		CanRegisterCustom: s.services.settings != nil,
		Registered:        agentManagementRegisteredItems(registered),
		Builtins:          agentManagementBuiltinItems(builtins, registeredKeys, installable),
		Installable:       agentManagementInstallItems(installable),
		Actions: []appviewmodel.AgentManagementAction{{
			ID:      agentActionRegisterCustom,
			Name:    "Register custom agent",
			Kind:    "register_custom",
			Enabled: s.services.settings != nil,
		}},
	}
	return appviewmodel.CloneAgentManagementView(view), nil
}

func agentManagementRegisteredItems(agents []AgentDescriptor) []appviewmodel.AgentManagementItem {
	agents = cloneAgents(agents)
	sort.Slice(agents, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(agents[i].Name, agents[i].ID)) < strings.ToLower(firstNonEmpty(agents[j].Name, agents[j].ID))
	})
	out := make([]appviewmodel.AgentManagementItem, 0, len(agents))
	for _, agent := range agents {
		out = append(out, appviewmodel.AgentManagementItem{
			Agent:      agentItemFromDescriptor(agent),
			Source:     "registered",
			Registered: true,
			Actions: []appviewmodel.AgentManagementAction{
				{ID: agentActionInvoke, Name: "Invoke", Kind: "invoke", Enabled: true},
				{ID: agentActionUseController, Name: "Use as controller", Kind: "controller", Enabled: true},
				{ID: agentActionRemove, Name: "Remove", Kind: "remove", Enabled: true, Destructive: true},
			},
		})
	}
	return out
}

func agentManagementBuiltinItems(builtins []AgentDescriptor, registeredKeys map[string]struct{}, installable []AgentInstallOption) []appviewmodel.AgentManagementItem {
	builtins = cloneAgents(builtins)
	sort.Slice(builtins, func(i, j int) bool {
		return strings.ToLower(firstNonEmpty(builtins[i].Name, builtins[i].ID)) < strings.ToLower(firstNonEmpty(builtins[j].Name, builtins[j].ID))
	})
	installableKeys := map[string]struct{}{}
	for _, option := range installable {
		if key := strings.ToLower(strings.TrimSpace(option.Value)); key != "" {
			installableKeys[key] = struct{}{}
		}
	}
	out := make([]appviewmodel.AgentManagementItem, 0, len(builtins))
	for _, agent := range builtins {
		key := strings.ToLower(firstNonEmpty(agent.Name, agent.ID))
		_, registered := registeredKeys[key]
		_, installable := installableKeys[key]
		actions := []appviewmodel.AgentManagementAction{{
			ID:      agentActionRegister,
			Name:    "Register",
			Kind:    "register",
			Enabled: !registered,
		}}
		if installable {
			actionID := agentActionInstall
			actionName := "Install"
			if registered {
				actionID = agentActionUpdate
				actionName = "Update"
			}
			actions = append(actions, appviewmodel.AgentManagementAction{
				ID:      actionID,
				Name:    actionName,
				Kind:    "install",
				Enabled: true,
			})
		}
		out = append(out, appviewmodel.AgentManagementItem{
			Agent:       agentItemFromDescriptor(agent),
			Source:      "builtin",
			Registered:  registered,
			Builtin:     true,
			Installable: installable,
			Actions:     actions,
		})
	}
	return out
}

func agentManagementInstallItems(options []AgentInstallOption) []appviewmodel.AgentInstallItem {
	if len(options) == 0 {
		return nil
	}
	options = append([]AgentInstallOption(nil), options...)
	sort.Slice(options, func(i, j int) bool {
		return strings.ToLower(options[i].Value) < strings.ToLower(options[j].Value)
	})
	out := make([]appviewmodel.AgentInstallItem, 0, len(options))
	for _, option := range options {
		id := strings.TrimSpace(option.Value)
		if id == "" {
			continue
		}
		out = append(out, appviewmodel.AgentInstallItem{
			ID:     id,
			Name:   firstNonEmpty(option.Display, id),
			Detail: strings.TrimSpace(option.Detail),
			Actions: []appviewmodel.AgentManagementAction{{
				ID:      agentActionInstall,
				Name:    "Install",
				Kind:    "install",
				Enabled: true,
			}},
		})
	}
	return out
}

func agentRegisteredKeys(agents []AgentDescriptor) map[string]struct{} {
	keys := map[string]struct{}{}
	for _, agent := range agents {
		for _, key := range []string{agent.ID, agent.Name, agentLookupKey(agent)} {
			key = strings.ToLower(strings.TrimSpace(key))
			if key != "" {
				keys[key] = struct{}{}
			}
		}
	}
	return keys
}

func agentItemFromDescriptor(agent AgentDescriptor) appviewmodel.AgentItem {
	agent = normalizeAgentDescriptor(agent)
	return appviewmodel.AgentItem{
		ID:          strings.TrimSpace(agent.ID),
		Name:        strings.TrimSpace(agent.Name),
		Kind:        strings.TrimSpace(string(agent.Kind)),
		Command:     strings.TrimSpace(agent.Command),
		Args:        append([]string(nil), agent.Args...),
		WorkDir:     strings.TrimSpace(agent.WorkDir),
		Description: strings.TrimSpace(agent.Description),
		Meta:        maps.Clone(agent.Meta),
	}
}
