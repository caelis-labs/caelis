package controlprompt

import (
	"reflect"
	"slices"
	"testing"
)

func TestRouterServiceMethodSetStaysConsumerFocused(t *testing.T) {
	typeOf := reflect.TypeFor[RouterService]()
	got := make([]string, 0, typeOf.NumMethod())
	for i := range typeOf.NumMethod() {
		got = append(got, typeOf.Method(i).Name)
	}
	want := []string{
		"AgentStatus",
		"Compact",
		"ContinueAgentRun",
		"ListSessions",
		"RepairSandbox",
		"ResetSession",
		"ResumeSession",
		"StartAgentRun",
		"StartReview",
		"Status",
		"Submit",
		"UseModel",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RouterService methods = %v, want exact router consumer set %v", got, want)
	}
}
