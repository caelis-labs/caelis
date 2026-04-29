package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestConnectEnterStartsInteractiveWizardAndIgnoresTypedArgs(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect openai-compatible")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if called != "" {
		t.Fatalf("ExecuteLine called with %q, want interactive wizard instead", called)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	if strings.TrimSpace(m.slashArgCommand) != "connect" {
		t.Fatalf("slashArgCommand = %q, want connect", m.slashArgCommand)
	}
	if got := m.textarea.Value(); got != "openai-compatible" {
		t.Fatalf("textarea = %q, want openai-compatible", got)
	}
	if got := strings.TrimSpace(m.slashArgQuery); got != "openai-compatible" {
		t.Fatalf("slashArgQuery = %q, want openai-compatible", got)
	}
}

func TestConnectEnterSubmitsWhenCommandUnavailable(t *testing.T) {
	called := ""
	m := NewModel(Config{
		Commands: []string{"help", "agent", "status", "resume", "model"},
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect openai-compatible")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if m.isWizardActive() {
		t.Fatal("connect wizard should not start when /connect is unavailable")
	}
	if called != "/connect openai-compatible" {
		t.Fatalf("ExecuteLine called with %q, want submitted ACP command", called)
	}
}

func TestConnectTypingTrailingSpaceDoesNotOpenGenericPicker(t *testing.T) {
	m := NewModel(Config{
		ExecuteLine:      func(Submission) TaskResultMsg { return TaskResultMsg{} },
		Wizards:          DefaultWizards(),
		SlashArgComplete: func(string, string, int) ([]SlashArgCandidate, error) { return nil, nil },
	})
	m.setInputText("/connect ")
	if len(m.slashArgCandidates) != 0 {
		t.Fatalf("unexpected slash arg candidates: %#v", m.slashArgCandidates)
	}
	if m.isWizardActive() {
		t.Fatal("wizard should not auto-open while still typing")
	}
}

func TestConnectWizardSkipsDirectlyToAPIKeyForMiniMax(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			case "connect-apikey:minimax":
				return nil, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:minimax" {
		t.Fatalf("slashArgCommand after minimax provider = %q, want connect-apikey:minimax", got)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after minimax provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardSkipsAPIKeyForNoAuthProvider(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "ollama", Display: "ollama", NoAuth: true}}, nil
			case "connect-model:ollama||60||":
				return []SlashArgCandidate{{Value: "qwen2.5:7b", Display: "ollama/qwen2.5:7b"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-model:ollama||60||" {
		t.Fatalf("slashArgCommand after ollama provider = %q, want connect-model:ollama||60||", got)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after no-auth provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardKeepsBaseURLStepForCompatibleProviders(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			case "connect-baseurl:openai-compatible":
				return []SlashArgCandidate{{Value: "https://api.openai.com/v1", Display: "https://api.openai.com/v1"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:openai-compatible" {
		t.Fatalf("slashArgCommand after openai-compatible provider = %q, want connect-baseurl:openai-compatible", got)
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea after openai-compatible provider = %q, want empty wizard input", got)
	}
}

func TestConnectWizardSkipsAdvancedStepsForKnownModelCandidate(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			case "connect-model:minimax||60|sk-test|":
				return []SlashArgCandidate{{Value: "MiniMax-M2.7-highspeed", Display: "minimax/MiniMax-M2.7-highspeed"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	handled, cmd := m.handleWizardEnter() // provider -> apikey
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "sk-test"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); !strings.HasPrefix(got, "connect-model:minimax|") {
		t.Fatalf("slashArgCommand after api key = %q, want connect-model:minimax|...", got)
	}
	handled, cmd = m.handleWizardEnter() // model -> submit
	if !handled {
		t.Fatal("model step was not handled")
	}
	if cmd == nil {
		t.Fatal("expected submit command after known model selection")
	}
	msg := cmd()
	if !findAndRunTaskResult(msg, m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	if called != "/connect minimax MiniMax-M2.7-highspeed - 60 sk-test - - -" {
		t.Fatalf("called = %q", called)
	}
}

func TestConnectWizardTypedKnownModelAlsoSkipsAdvancedSteps(t *testing.T) {
	called := ""
	m := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "minimax", Display: "minimax"}}, nil
			case "connect-model:minimax||60|sk-test|":
				return []SlashArgCandidate{{Value: "MiniMax-M2.7-highspeed", Display: "minimax/MiniMax-M2.7-highspeed"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	handled, cmd := m.handleWizardEnter() // provider -> apikey
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "sk-test"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	m.slashArgQuery = "minimax/MiniMax-M2.7-highspeed"
	handled, cmd = m.handleWizardEnter() // typed display match -> submit
	if !handled {
		t.Fatal("typed model step was not handled")
	}
	if cmd == nil {
		t.Fatal("expected submit command after known model typed value")
	}
	msg := cmd()
	if !findAndRunTaskResult(msg, m) {
		t.Fatal("expected TaskResultMsg in batch")
	}
	if called != "/connect minimax MiniMax-M2.7-highspeed - 60 sk-test - - -" {
		t.Fatalf("called = %q", called)
	}
}

func findAndRunTaskResult(msg tea.Msg, m *Model) bool {
	if _, ok := msg.(TaskResultMsg); ok {
		m.Update(msg)
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if cmd == nil {
				continue
			}
			subMsg := cmd()
			if subMsg == nil {
				continue
			}
			if findAndRunTaskResult(subMsg, m) {
				return true
			}
		}
	}
	return false
}
