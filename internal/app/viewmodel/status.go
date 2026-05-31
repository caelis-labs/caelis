package viewmodel

import (
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/session"
)

type StatusView struct {
	Runtime   RuntimeStatus  `json:"runtime"`
	Session   *SessionStatus `json:"session,omitempty"`
	Model     ModelStatus    `json:"model"`
	Mode      ModeStatus     `json:"mode"`
	Agents    AgentStatus    `json:"agents"`
	Resources ResourceStatus `json:"resources"`
	Usage     UsageStatus    `json:"usage,omitempty"`
}

type RuntimeStatus struct {
	AppName                  string `json:"app_name,omitempty"`
	UserID                   string `json:"user_id,omitempty"`
	WorkspaceKey             string `json:"workspace_key,omitempty"`
	WorkspaceCWD             string `json:"workspace_cwd,omitempty"`
	DefaultModel             string `json:"default_model,omitempty"`
	StoreBackend             string `json:"store_backend,omitempty"`
	StoreURI                 string `json:"store_uri,omitempty"`
	SandboxBackend           string `json:"sandbox_backend,omitempty"`
	SandboxNetwork           string `json:"sandbox_network,omitempty"`
	SandboxReadableRootCount int    `json:"sandbox_readable_root_count,omitempty"`
	SandboxWritableRootCount int    `json:"sandbox_writable_root_count,omitempty"`
}

type SessionStatus struct {
	Ref                  session.Ref       `json:"ref"`
	Title                string            `json:"title,omitempty"`
	Workspace            session.Workspace `json:"workspace,omitempty"`
	Status               string            `json:"status,omitempty"`
	UpdatedAt            time.Time         `json:"updated_at,omitempty"`
	TranscriptCount      int               `json:"transcript_count,omitempty"`
	PlanCount            int               `json:"plan_count,omitempty"`
	PendingApprovalCount int               `json:"pending_approval_count,omitempty"`
	ParticipantCount     int               `json:"participant_count,omitempty"`
}

type ModelStatus struct {
	Configured      bool          `json:"configured"`
	Count           int           `json:"count,omitempty"`
	Current         *ModelChoice  `json:"current,omitempty"`
	Choices         []ModelChoice `json:"choices,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	MissingAPIKey   bool          `json:"missing_api_key,omitempty"`
}

type ModelChoice struct {
	ID         string `json:"id,omitempty"`
	Alias      string `json:"alias,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	ProfileID  string `json:"profile_id,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Default    bool   `json:"default,omitempty"`
}

type ModeStatus struct {
	Current ModeChoice   `json:"current"`
	Choices []ModeChoice `json:"choices,omitempty"`
}

type ModeChoice struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type AgentStatus struct {
	Count            int         `json:"count,omitempty"`
	ExternalACPCount int         `json:"external_acp_count,omitempty"`
	Items            []AgentItem `json:"items,omitempty"`
}

type AgentItem struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Kind        string            `json:"kind,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Description string            `json:"description,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

type ResourceStatus struct {
	Plugins        int                  `json:"plugins,omitempty"`
	ModelProviders int                  `json:"model_providers,omitempty"`
	Stores         int                  `json:"stores,omitempty"`
	Sandboxes      int                  `json:"sandbox_backends,omitempty"`
	Tools          int                  `json:"tools,omitempty"`
	Prompts        int                  `json:"prompts,omitempty"`
	Skills         int                  `json:"skills,omitempty"`
	ACPAgents      int                  `json:"acp_agents,omitempty"`
	RendererHints  int                  `json:"renderer_hints,omitempty"`
	AgentFiles     int                  `json:"agent_files,omitempty"`
	InfoCount      int                  `json:"info_count,omitempty"`
	WarningCount   int                  `json:"warning_count,omitempty"`
	ErrorCount     int                  `json:"error_count,omitempty"`
	Diagnostics    []ResourceDiagnostic `json:"diagnostics,omitempty"`
}

type ResourceDiagnostic struct {
	Severity string            `json:"severity,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	ID       string            `json:"id,omitempty"`
	Path     string            `json:"path,omitempty"`
	Message  string            `json:"message,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

type UsageStatus struct {
	Total         TokenUsage    `json:"total,omitempty"`
	Main          TokenUsage    `json:"main,omitempty"`
	Subagents     TokenUsage    `json:"subagents,omitempty"`
	AutoReview    TokenUsage    `json:"auto_review,omitempty"`
	Compaction    TokenUsage    `json:"compaction,omitempty"`
	ContextBudget ContextBudget `json:"context_budget,omitempty"`
}

type TokenUsage struct {
	InputTokens         int `json:"input_tokens,omitempty"`
	CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens,omitempty"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	ContextWindowTokens int `json:"context_window_tokens,omitempty"`
}

func NormalizeTokenUsage(usage TokenUsage) TokenUsage {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.ReasoningTokens < 0 {
		usage.ReasoningTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	if usage.ContextWindowTokens < 0 {
		usage.ContextWindowTokens = 0
	}
	if usage.TotalTokens == 0 && (usage.InputTokens != 0 || usage.OutputTokens != 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func TokenUsageZero(usage TokenUsage) bool {
	return usage.InputTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0 &&
		usage.ContextWindowTokens == 0
}

func AddTokenUsage(total *TokenUsage, usage TokenUsage) {
	if total == nil {
		return
	}
	usage = NormalizeTokenUsage(usage)
	total.InputTokens += usage.InputTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.OutputTokens += usage.OutputTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
	if usage.ContextWindowTokens > total.ContextWindowTokens {
		total.ContextWindowTokens = usage.ContextWindowTokens
	}
}

type ContextBudget struct {
	Source                    string `json:"source,omitempty"`
	ModelID                   string `json:"model_id,omitempty"`
	Provider                  string `json:"provider,omitempty"`
	Model                     string `json:"model,omitempty"`
	AsOfEventID               string `json:"as_of_event_id,omitempty"`
	LastCompactEventID        string `json:"last_compact_event_id,omitempty"`
	PostCompact               bool   `json:"post_compact,omitempty"`
	MessageCount              int    `json:"message_count,omitempty"`
	ContextWindowTokens       int    `json:"context_window_tokens,omitempty"`
	MaxOutputTokens           int    `json:"max_output_tokens,omitempty"`
	EffectiveInputBudget      int    `json:"effective_input_budget,omitempty"`
	EstimatedInputTokens      int    `json:"estimated_input_tokens,omitempty"`
	EstimatedHistoryTokens    int    `json:"estimated_history_tokens,omitempty"`
	EstimatedPrefixTokens     int    `json:"estimated_prefix_tokens,omitempty"`
	EstimatedRemainingTokens  int    `json:"estimated_remaining_tokens,omitempty"`
	EstimatedOverBudgetTokens int    `json:"estimated_over_budget_tokens,omitempty"`
}

func RuntimeStatusFromConfig(runtime config.Runtime) RuntimeStatus {
	return RuntimeStatus{
		AppName:                  runtime.AppName,
		UserID:                   runtime.UserID,
		WorkspaceKey:             runtime.WorkspaceKey,
		WorkspaceCWD:             runtime.WorkspaceCWD,
		DefaultModel:             runtime.Model,
		StoreBackend:             runtime.Store.Backend,
		StoreURI:                 runtime.Store.URI,
		SandboxBackend:           runtime.Sandbox.Backend,
		SandboxNetwork:           runtime.Sandbox.Network,
		SandboxReadableRootCount: len(runtime.Sandbox.ReadableRoots),
		SandboxWritableRootCount: len(runtime.Sandbox.WritableRoots),
	}
}

func SessionStatusFromView(view SessionView) SessionStatus {
	return SessionStatus{
		Ref:                  view.Ref,
		Title:                view.Title,
		Workspace:            view.Workspace,
		Status:               view.Status,
		UpdatedAt:            view.UpdatedAt,
		TranscriptCount:      len(view.Transcript),
		PlanCount:            len(view.Plan),
		PendingApprovalCount: len(view.PendingApprovals),
		ParticipantCount:     len(view.Participants),
	}
}
