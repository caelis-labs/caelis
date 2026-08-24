package tuiapp

import "testing"

func TestHistoricalAliasesStayGenericInSurfacePresentation(t *testing.T) {
	t.Parallel()

	if surfaceIsExplorationTool("SEARCH", "", "") {
		t.Fatal("SEARCH alias unexpectedly acquired exploration presentation")
	}
	if surfaceIsTerminalPanelTool("RUN_COMMAND") {
		t.Fatal("RUN_COMMAND alias unexpectedly acquired terminal-panel presentation")
	}
	if surfaceIsTerminalPanelTool("execute") {
		t.Fatal("generic execute kind unexpectedly acquired terminal-panel presentation")
	}
}
