package acp

// ─── Terminal lifecycle types ─────────────────────────────────────────

// CreateTerminalRequest starts a new terminal session.
type CreateTerminalRequest struct {
	SessionID       string        `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitempty"`
	CWD             string        `json:"cwd,omitempty"`
	Env             []EnvVariable `json:"env,omitempty"`
	OutputByteLimit *int          `json:"outputByteLimit,omitempty"`
}

// EnvVariable is an environment variable.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CreateTerminalResponse is the response to CreateTerminalRequest.
type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

// TerminalOutputRequest reads output from a terminal.
type TerminalOutputRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// TerminalOutputResponse is the response to TerminalOutputRequest.
type TerminalOutputResponse struct {
	Output     string              `json:"output"`
	Truncated  bool                `json:"truncated"`
	ExitStatus *TerminalExitStatus `json:"exitStatus,omitempty"`
}

// TerminalExitStatus describes how a terminal exited.
type TerminalExitStatus struct {
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

// TerminalWaitForExitRequest waits for a terminal to exit.
type TerminalWaitForExitRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// TerminalWaitForExitResponse is the response to WaitForExit.
type TerminalWaitForExitResponse struct {
	ExitCode *int    `json:"exitCode,omitempty"`
	Signal   *string `json:"signal,omitempty"`
}

// TerminalKillRequest kills a terminal.
type TerminalKillRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

// TerminalReleaseRequest releases a terminal.
type TerminalReleaseRequest struct {
	SessionID  string `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}
