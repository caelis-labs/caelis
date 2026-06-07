package agent

import "testing"

func TestRunConfigValidate(t *testing.T) {
	var nilCfg *RunConfig
	if err := nilCfg.Validate(); err != nil {
		t.Errorf("nil config should be valid, got %v", err)
	}

	valid := &RunConfig{MaxModelCalls: 10, MaxToolCalls: 20}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	neg := &RunConfig{MaxModelCalls: -1}
	if err := neg.Validate(); err == nil {
		t.Error("expected error for negative MaxModelCalls")
	}

	negTool := &RunConfig{MaxToolCalls: -1}
	if err := negTool.Validate(); err == nil {
		t.Error("expected error for negative MaxToolCalls")
	}
}

func TestDefaultRunConfig(t *testing.T) {
	cfg := DefaultRunConfig()
	if cfg.MaxModelCalls != 100 {
		t.Errorf("got %d, want 100", cfg.MaxModelCalls)
	}
	if cfg.MaxToolCalls != 100 {
		t.Errorf("got %d, want 100", cfg.MaxToolCalls)
	}
	if cfg.Metadata == nil {
		t.Error("expected Metadata to be initialized")
	}
}
