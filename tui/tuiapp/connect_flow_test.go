package tuiapp

import (
	"net/url"
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

func TestConnectWizardSkipsAPIKeyForReusableBaseURLAuth(t *testing.T) {
	const baseURL = "https://api.openai.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "openai-compatible", Display: "openai-compatible"}}, nil
			case "connect-baseurl:openai-compatible":
				return []SlashArgCandidate{{Value: baseURL, Display: baseURL, Detail: "configured auth", NoAuth: true}}, nil
			case "connect-model:openai-compatible|" + url.QueryEscape(baseURL) + "|60||":
				return []SlashArgCandidate{{Value: "gpt-4o-mini", Display: "openai-compatible/gpt-4o-mini"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter() // provider -> baseurl
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	handled, cmd = m.handleWizardEnter() // reusable base URL -> model
	if !handled {
		t.Fatal("baseurl selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-model:openai-compatible|"+url.QueryEscape(baseURL)+"|60||" {
		t.Fatalf("slashArgCommand after reusable baseurl = %q, want model step without API key", got)
	}
}

func TestConnectWizardTypedXiaomiAdvancesToEndpointStep(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: "https://token-plan-cn.xiaomimimo.com/v1", Display: "token plan cn"},
				}, nil
			default:
				return nil, nil
			}
		},
	})
	m.setInputText("/connect xiaomi")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
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
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after typed xiaomi provider = %q, want connect-baseurl:xiaomi", got)
	}
	if got := m.wizardHintText(); !strings.Contains(got, "/connect endpoint") {
		t.Fatalf("wizard hint = %q, want endpoint hint", got)
	}
}

func TestConnectWizardPrefixSelectsXiaomiCandidateAndKeepsModelCandidates(t *testing.T) {
	const tokenPlanBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: tokenPlanBaseURL, Display: "token plan cn"},
				}, nil
			case "connect-apikey:xiaomi":
				return nil, nil
			case "connect-model:xiaomi|" + url.QueryEscape(tokenPlanBaseURL) + "|60|sk-test|":
				return []SlashArgCandidate{{Value: "mimo-v2.5-pro", Display: "xiaomi/mimo-v2.5-pro"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.setInputText("/connect x")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider prefix selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi prefix = %q, want connect-baseurl:xiaomi", got)
	}

	m.setInputText("token")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd = m.handleWizardEnter()
	if !handled {
		t.Fatal("endpoint prefix selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:xiaomi" {
		t.Fatalf("slashArgCommand after token-plan prefix = %q, want connect-apikey:xiaomi", got)
	}

	m.setInputText("sk-test")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
	handled, cmd = m.handleWizardEnter()
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	wantCommand := "connect-model:xiaomi|" + url.QueryEscape(tokenPlanBaseURL) + "|60|sk-test|"
	if got := strings.TrimSpace(m.slashArgCommand); got != wantCommand {
		t.Fatalf("slashArgCommand after api key = %q, want %q", got, wantCommand)
	}
	if len(m.slashArgCandidates) == 0 || m.slashArgCandidates[0].Value != "mimo-v2.5-pro" {
		t.Fatalf("model candidates after api key = %#v, want mimo-v2.5-pro", m.slashArgCandidates)
	}
}

func TestConnectWizardDoesNotAcceptUnknownProviderFreeform(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "connect" {
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			}
			return nil, nil
		},
	})
	m.setInputText("/connect xiaomi token")
	m.syncTextareaFromInput()
	_, cmd := m.Update(keyPress("enter"))
	if cmd != nil {
		findAndRunTaskResult(cmd(), m)
	}
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter()
	if !handled {
		t.Fatal("provider step enter was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect" {
		t.Fatalf("slashArgCommand after unknown provider input = %q, want to stay on connect provider step", got)
	}
	if got := strings.TrimSpace(m.wizard.state["provider"]); got != "" {
		t.Fatalf("provider state = %q, want empty after rejected provider input", got)
	}
}

func TestConnectWizardAddsEndpointStepForXiaomiTokenPlan(t *testing.T) {
	const tokenPlanBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
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
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{
					{Value: "https://api.xiaomimimo.com/v1", Display: "api cn"},
					{Value: tokenPlanBaseURL, Display: "token plan cn"},
				}, nil
			case "connect-apikey:xiaomi":
				return nil, nil
			case "connect-model:xiaomi|" + url.QueryEscape(tokenPlanBaseURL) + "|60|" + url.QueryEscape("env:MIMO_TOKEN_PLAN_API_KEY") + "|":
				return []SlashArgCandidate{{Value: "mimo-v2.5-pro", Display: "xiaomi/mimo-v2.5-pro"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	if !m.isWizardActive() {
		t.Fatal("expected connect wizard to start")
	}
	handled, cmd := m.handleWizardEnter() // provider -> endpoint
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-baseurl:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi provider = %q, want connect-baseurl:xiaomi", got)
	}

	m.slashArgIndex = 1
	handled, cmd = m.handleWizardEnter() // token plan endpoint -> api key
	if !handled {
		t.Fatal("endpoint selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-apikey:xiaomi" {
		t.Fatalf("slashArgCommand after xiaomi endpoint = %q, want connect-apikey:xiaomi", got)
	}

	m.slashArgQuery = "env:MIMO_TOKEN_PLAN_API_KEY"
	handled, cmd = m.handleWizardEnter() // api key -> model
	if !handled {
		t.Fatal("apikey step was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); !strings.HasPrefix(got, "connect-model:xiaomi|"+url.QueryEscape(tokenPlanBaseURL)+"|60|") {
		t.Fatalf("slashArgCommand after token-plan api key = %q, want connect-model:xiaomi|token-plan...", got)
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
	want := "/connect xiaomi mimo-v2.5-pro " + tokenPlanBaseURL + " 60 env:MIMO_TOKEN_PLAN_API_KEY - - -"
	if called != want {
		t.Fatalf("called = %q, want %q", called, want)
	}
}

func TestConnectWizardSkipsAPIKeyForReusableEndpointAuth(t *testing.T) {
	const apiBaseURL = "https://api.xiaomimimo.com/v1"
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		SlashArgComplete: func(command string, _ string, _ int) ([]SlashArgCandidate, error) {
			switch command {
			case "connect":
				return []SlashArgCandidate{{Value: "xiaomi", Display: "xiaomi"}}, nil
			case "connect-baseurl:xiaomi":
				return []SlashArgCandidate{{Value: apiBaseURL, Display: "api cn", Detail: "configured auth", NoAuth: true}}, nil
			case "connect-model:xiaomi|" + url.QueryEscape(apiBaseURL) + "|60||":
				return []SlashArgCandidate{{Value: "mimo-v2-pro", Display: "xiaomi/mimo-v2-pro"}}, nil
			default:
				return nil, nil
			}
		},
	})
	m.openSlashArgPicker("connect")
	handled, cmd := m.handleWizardEnter() // provider -> endpoint
	if !handled {
		t.Fatal("provider selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	handled, cmd = m.handleWizardEnter() // reusable endpoint -> model
	if !handled {
		t.Fatal("endpoint selection was not handled")
	}
	if cmd != nil {
		cmd()
	}
	if got := strings.TrimSpace(m.slashArgCommand); got != "connect-model:xiaomi|"+url.QueryEscape(apiBaseURL)+"|60||" {
		t.Fatalf("slashArgCommand after reusable endpoint = %q, want model step without API key", got)
	}
}

func TestConnectWizardAPIKeyHintUsesSelectedXiaomiEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantEnv string
	}{
		{name: "api cn", baseURL: "https://api.xiaomimimo.com/v1", wantEnv: "XIAOMI_API_KEY"},
		{name: "token plan cn", baseURL: "https://token-plan-cn.xiaomimimo.com/v1", wantEnv: "MIMO_TOKEN_PLAN_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel(Config{
				Wizards: DefaultWizards(),
			})
			def := m.findWizard("connect")
			if def == nil {
				t.Fatalf("connect wizard not found")
			}
			m.wizard = &wizardRuntime{
				def:       def,
				stepIndex: 3,
				state: map[string]string{
					"provider": "xiaomi",
					"baseurl":  tt.baseURL,
				},
			}
			got := m.wizardHintText()
			if !strings.Contains(got, "env:"+tt.wantEnv) {
				t.Fatalf("wizard hint = %q, want %s", got, tt.wantEnv)
			}
			if strings.Contains(got, "OPENAI_API_KEY") {
				t.Fatalf("wizard hint = %q, should not mention OPENAI_API_KEY", got)
			}
		})
	}
}

func TestConnectWizardAPIKeyHintRecognizesCustomXiaomiTokenPlanHost(t *testing.T) {
	m := NewModel(Config{
		Wizards: DefaultWizards(),
	})
	def := m.findWizard("connect")
	if def == nil {
		t.Fatalf("connect wizard not found")
	}
	m.wizard = &wizardRuntime{
		def:       def,
		stepIndex: 3,
		state: map[string]string{
			"provider": "xiaomi",
			"baseurl":  "https://token-plan-cn.xiaomimimo.com/custom/v1",
		},
	}
	got := m.wizardHintText()
	if !strings.Contains(got, "env:MIMO_TOKEN_PLAN_API_KEY") {
		t.Fatalf("wizard hint = %q, want token-plan env hint", got)
	}
	if strings.Contains(got, "env:XIAOMI_API_KEY") {
		t.Fatalf("wizard hint = %q, should not prefer generic Xiaomi env", got)
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
