package control

import (
	"maps"
	"strings"
	"time"
)

type SubmissionMode string

const (
	SubmissionModeDefault SubmissionMode = ""
	SubmissionModeOverlay SubmissionMode = "overlay"
)

type Attachment struct {
	Name   string
	Offset int
}

type Submission struct {
	Text        string
	DisplayText string
	Mode        SubmissionMode
	Attachments []Attachment
}

type UsageSnapshot struct {
	PromptTokens      int
	CachedInputTokens int
	CompletionTokens  int
	ReasoningTokens   int
	TotalTokens       int
}

type ModelUsageSnapshot struct {
	Provider string
	Model    string
	Usage    UsageSnapshot
}

type SessionSnapshot struct {
	SessionID string
}

type SandboxSetupStatus struct {
	Required bool
	Error    string
	Details  map[string]string
	Counts   map[string]int
	Checks   []SandboxSetupCheck
}

type SandboxSetupCheck struct {
	Name      string
	Scope     string
	Current   bool
	Required  bool
	Reason    string
	Error     string
	Version   int
	Root      string
	UpdatedAt time.Time
	Details   map[string]string
	Counts    map[string]int
}

func CloneSandboxSetupStatus(in SandboxSetupStatus) SandboxSetupStatus {
	out := in
	out.Error = strings.TrimSpace(in.Error)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	if len(in.Checks) > 0 {
		out.Checks = make([]SandboxSetupCheck, len(in.Checks))
		for i, check := range in.Checks {
			out.Checks[i] = CloneSandboxSetupCheck(check)
		}
	}
	return out
}

func CloneSandboxSetupCheck(in SandboxSetupCheck) SandboxSetupCheck {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Scope = strings.TrimSpace(in.Scope)
	out.Reason = strings.TrimSpace(in.Reason)
	out.Error = strings.TrimSpace(in.Error)
	out.Root = strings.TrimSpace(in.Root)
	out.Details = cloneTrimmedStringMap(in.Details)
	out.Counts = maps.Clone(in.Counts)
	return out
}

func (s SandboxSetupStatus) Check(name string) (SandboxSetupCheck, bool) {
	name = strings.TrimSpace(name)
	for _, check := range s.Checks {
		if strings.TrimSpace(check.Name) == name {
			return CloneSandboxSetupCheck(check), true
		}
	}
	return SandboxSetupCheck{}, false
}

func cloneTrimmedStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

type StatusSnapshot struct {
	SessionID                       string
	Workspace                       string
	StoreDir                        string
	Model                           string
	ReasoningEffort                 string
	Provider                        string
	ModelName                       string
	ModeLabel                       string
	SessionMode                     string
	SandboxType                     string
	SandboxRequestedBackend         string
	SandboxResolvedBackend          string
	Route                           string
	FallbackReason                  string
	SandboxInstallHint              string
	SandboxSetup                    SandboxSetupStatus
	SandboxSetupRequired            bool
	SandboxSetupError               string
	SandboxSetupMarkerCurrent       bool
	SandboxSetupMarkerReason        string
	SandboxGlobalSetupCurrent       bool
	SandboxGlobalSetupRequired      bool
	SandboxGlobalSetupReason        string
	SandboxWorkspaceSetupCurrent    bool
	SandboxWorkspaceSetupRequired   bool
	SandboxWorkspaceSetupReason     string
	SandboxWorkspaceSetupRoot       string
	SandboxWorkspaceSetupWriteRoots int
	SandboxWorkspaceSetupPolicyHash string
	SandboxWorkspaceSetupUpdatedAt  time.Time
	SecuritySummary                 string
	MissingAPIKey                   bool
	HostExecution                   bool
	FullAccessMode                  bool
	Surface                         string
	PromptTokens                    int
	CompletionTokens                int
	TotalTokens                     int
	ContextWindowTokens             int
	SessionUsageTotal               UsageSnapshot
	SessionUsageMain                UsageSnapshot
	SessionUsageSubagents           UsageSnapshot
	SessionUsageAutoReview          UsageSnapshot
	SessionUsageByModel             []ModelUsageSnapshot
	SessionInputTokens              int
	SessionCachedInputTokens        int
	SessionOutputTokens             int
	SessionReasoningTokens          int
	SessionTotalTokens              int
	ActiveJobs                      int
	ActiveTurnKind                  string
	Running                         bool
}

type ResumeCandidate struct {
	SessionID string
	Title     string
	Prompt    string
	Model     string
	Workspace string
	Age       string
	UpdatedAt time.Time
}

type CompletionCandidate struct {
	Value   string
	Display string
	Detail  string
	Path    string
}

type SlashArgCandidate struct {
	Value   string
	Display string
	Detail  string
	NoAuth  bool
}

type AgentCandidate struct {
	Name        string
	Description string
}

type AgentParticipantSnapshot struct {
	ID        string
	Label     string
	AgentName string
	Kind      string
	Role      string
	SessionID string
}

type AgentStatusSnapshot struct {
	SessionID                 string
	ControllerKind            string
	ControllerLabel           string
	ControllerEpoch           string
	ControllerModel           string
	ControllerReasoningEffort string
	ControllerCommands        []string
	ControllerModels          []SlashArgCandidate
	ControllerEfforts         []SlashArgCandidate
	HasActiveTurn             bool
	ActiveTurnKind            string
	AvailableAgents           []AgentCandidate
	Participants              []AgentParticipantSnapshot
	DelegatedParticipants     []AgentParticipantSnapshot
}

type AgentProfileSnapshot struct {
	ID              string
	Name            string
	Description     string
	Capabilities    []string
	Path            string
	Enabled         bool
	Target          string
	Model           string
	ACPAgent        string
	ACPModel        string
	ReasoningEffort string
	Status          string
	Warning         string
	Source          string
	BuiltIn         bool
	SystemManaged   bool
}

type AgentProfileStatusSnapshot struct {
	Profiles []AgentProfileSnapshot
	Warnings []string
}

type AgentProfileBindingConfig struct {
	ProfileID       string
	Target          string
	Model           string
	ACPAgent        string
	ACPModel        string
	ReasoningEffort string
}

type CustomAgentConfig struct {
	Name        string
	Description string
	Command     string
	Args        []string
	Env         map[string]string
	WorkDir     string
}

type AgentAddOptions struct {
	Install bool
	Custom  *CustomAgentConfig
}

type ConnectConfig struct {
	Provider                       string
	EndpointID                     string
	Model                          string
	BaseURL                        string
	TimeoutSeconds                 int
	StreamFirstEventTimeoutSeconds int
	APIKey                         string
	TokenEnv                       string
	AuthType                       string
	ContextWindowTokens            int
	MaxOutputTokens                int
	ReasoningEffort                string
	ReasoningLevels                []string
}

type ApprovalDecision struct {
	Outcome    string
	OptionID   string
	Approved   bool
	Reason     string
	ReviewText string
}

type MCPServerSnapshot struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Tools   []string `json:"tools,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

type PluginSnapshot struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description"`
	Root        string              `json:"root"`
	Enabled     bool                `json:"enabled"`
	Skills      []string            `json:"skills"`
	Hooks       []string            `json:"hooks"`
	Agents      []string            `json:"agents,omitempty"`
	MCPServers  []MCPServerSnapshot `json:"mcp_servers,omitempty"`
	Status      string              `json:"status"`
	Warning     string              `json:"warning,omitempty"`
}

type MarketplaceSnapshot struct {
	Name                              string   `json:"name"`
	Description                       string   `json:"description,omitempty"`
	Owner                             string   `json:"owner,omitempty"`
	Source                            string   `json:"source,omitempty"`
	Root                              string   `json:"root,omitempty"`
	Version                           string   `json:"version,omitempty"`
	PluginRoot                        string   `json:"plugin_root,omitempty"`
	AllowCrossMarketplaceDependencies []string `json:"allow_cross_marketplace_dependencies,omitempty"`
	PluginCount                       int      `json:"plugin_count,omitempty"`
}
