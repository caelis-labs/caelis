package tool

// Observer is notified of tool execution events. Implementations include
// logging, telemetry, and approval routing.
type Observer interface {
	// BeforeTool is called before a tool executes.
	BeforeTool(call Call)

	// AfterTool is called after a tool completes.
	AfterTool(call Call, result Result, err error)
}
