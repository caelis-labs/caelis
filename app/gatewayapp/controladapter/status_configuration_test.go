package controladapter

import (
	"context"
	"errors"
	"testing"
)

func TestStatusProjectsCanonicalConfigurationRevision(t *testing.T) {
	driver := NewStatusAssemblerForHost(&ControlRuntimeDeps{Status: StatusRuntimeDeps{
		ConfigurationRevisionFn: func(context.Context) (uint64, error) { return 42, nil },
	}}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Configuration.Revision != 42 {
		t.Fatalf("configuration revision = %d, want 42", status.Configuration.Revision)
	}
}

func TestStatusPropagatesConfigurationRevisionReadFailure(t *testing.T) {
	fault := errors.New("config read failed")
	driver := NewStatusAssemblerForHost(&ControlRuntimeDeps{Status: StatusRuntimeDeps{
		ConfigurationRevisionFn: func(context.Context) (uint64, error) { return 0, fault },
	}}, "test", "")
	if _, err := driver.LightweightStatus(context.Background()); !errors.Is(err, fault) {
		t.Fatalf("LightweightStatus() error = %v, want %v", err, fault)
	}
}

func TestStatusProjectsEffectiveProcessModelWithoutSession(t *testing.T) {
	driver := NewStatusAssemblerForHost(&ControlRuntimeDeps{Model: ModelRuntimeDeps{
		EffectiveAliasFn:  func() string { return "xiaomi/mimo-v2.5-pro" },
		EffectiveEffortFn: func() string { return "high" },
	}}, "test", "")
	status, err := driver.LightweightStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Session.ID != "" || status.ModelStatus.Display != "xiaomi/mimo-v2.5-pro [high]" ||
		status.ModelStatus.ReasoningEffort != "high" {
		t.Fatalf("Host effective model status = %#v", status)
	}
}
