// Package names declares the exact Definition.Name values shared by built-in
// tool definitions and their consumers. These constants identify names only.
// Runtime establishes execution authority from the bound tool and its policy.
package names

const (
	// Glob is the name of the file pattern search tool.
	Glob = "Glob"
	// Grep is the name of the file content search tool.
	Grep = "Grep"
	// Patch is the name of the file patch tool.
	Patch = "Patch"
	// Plan is the name of the Session plan tool.
	Plan = "Plan"
	// Read is the name of the file reading tool.
	Read = "Read"
	// RunCommand is the name of the command execution tool.
	RunCommand = "RunCommand"
	// SendMessage is the name of the Agent messaging tool.
	SendMessage = "SendMessage"
	// Skill is the name of the skill loading tool.
	Skill = "Skill"
	// StartThread is the name of the Agent delegation tool.
	StartThread = "StartThread"
	// Task is the name of the Task observation and control tool.
	Task = "Task"
	// ViewImage is the name of the image viewing tool.
	ViewImage = "ViewImage"
	// WebFetch is the name of the web page fetching tool.
	WebFetch = "WebFetch"
	// WebSearch is the name of the web search tool.
	WebSearch = "WebSearch"
	// Write is the name of the file writing tool.
	Write = "Write"
)
