package codex

import (
	"strings"
	"sync"

	acp "github.com/caelis-labs/acp-go-sdk"
)

const (
	configIDModel  = "model"
	configIDEffort = "reasoning_effort"
)

type codexModel struct {
	ID                     string `json:"id"`
	Model                  string `json:"model"`
	DisplayName            string `json:"displayName"`
	Description            string `json:"description"`
	Hidden                 bool   `json:"hidden"`
	DefaultReasoningEffort string `json:"defaultReasoningEffort"`
	SupportedEfforts       []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
}

type threadOpenResponse struct {
	Thread struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"thread"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
}

type sessionState struct {
	mu       sync.Mutex
	steerMu  sync.Mutex
	promptMu sync.Mutex

	threadID string
	cwd      string
	roots    []string
	model    string
	effort   string
	models   []codexModel

	activeTurnID string
	turnDone     chan turnResult
	route        *sessionRoute
}

type turnResult struct {
	stopReason acp.StopReason
	err        error
}

func (s *sessionState) applyOpenResponse(response threadOpenResponse) {
	s.mu.Lock()
	s.model = strings.TrimSpace(response.Model)
	s.effort = strings.TrimSpace(response.ReasoningEffort)
	s.mu.Unlock()
}

func (s *sessionState) clearTurn(done chan turnResult) {
	s.mu.Lock()
	if s.turnDone == done {
		s.turnDone = nil
		s.activeTurnID = ""
	}
	s.mu.Unlock()
}

func (s *sessionState) hasModel(value string) bool {
	for _, model := range s.models {
		if modelName(model) == value {
			return true
		}
	}
	return false
}

func (s *sessionState) hasEffort(value string) bool {
	for _, model := range s.models {
		if modelName(model) != s.model {
			continue
		}
		for _, effort := range model.SupportedEfforts {
			if strings.EqualFold(strings.TrimSpace(effort.ReasoningEffort), value) {
				return true
			}
		}
	}
	return false
}

func (s *sessionState) selectDefaultEffortLocked() {
	for _, model := range s.models {
		if modelName(model) == s.model {
			s.effort = strings.TrimSpace(model.DefaultReasoningEffort)
			return
		}
	}
}

func (s *sessionState) configOptions() []acp.SessionConfigOption {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configOptionsLocked()
}

func (s *sessionState) configOptionsLocked() []acp.SessionConfigOption {
	var options []acp.SessionConfigOption
	if len(s.models) > 0 {
		values := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(s.models))
		for _, model := range s.models {
			name := modelName(model)
			if name == "" || model.Hidden {
				continue
			}
			description := strings.TrimSpace(model.Description)
			values = append(values, acp.SessionConfigSelectOption{
				Value: acp.SessionConfigValueId(name), Name: firstNonEmpty(model.DisplayName, name),
				Description: optionalString(description),
			})
		}
		if len(values) > 0 {
			modelOption := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(s.model), acp.SessionConfigSelectOptions{Ungrouped: &values})
			modelOption.Select.Id = acp.SessionConfigId(configIDModel)
			modelOption.Select.Name = "Model"
			category := acp.SessionConfigOptionCategoryModel
			modelOption.Select.Category = &category
			options = append(options, modelOption)
		}
	}
	var efforts acp.SessionConfigSelectOptionsUngrouped
	for _, model := range s.models {
		if modelName(model) != s.model {
			continue
		}
		for _, effort := range model.SupportedEfforts {
			value := strings.TrimSpace(effort.ReasoningEffort)
			if value == "" {
				continue
			}
			efforts = append(efforts, acp.SessionConfigSelectOption{
				Value: acp.SessionConfigValueId(value), Name: titleWord(value),
				Description: optionalString(strings.TrimSpace(effort.Description)),
			})
		}
	}
	if len(efforts) > 0 {
		effortOption := acp.NewSessionConfigOptionSelect(acp.SessionConfigValueId(s.effort), acp.SessionConfigSelectOptions{Ungrouped: &efforts})
		effortOption.Select.Id = acp.SessionConfigId(configIDEffort)
		effortOption.Select.Name = "Reasoning effort"
		category := acp.SessionConfigOptionCategoryThoughtLevel
		effortOption.Select.Category = &category
		options = append(options, effortOption)
	}
	return options
}

func modelName(model codexModel) string {
	return firstNonEmpty(strings.TrimSpace(model.Model), strings.TrimSpace(model.ID))
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func titleWord(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
