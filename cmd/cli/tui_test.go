package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestTUIProgramOptionsOnlyAddsFPSWhenConfigured(t *testing.T) {
	defaultOptions := tuiProgramOptions(strings.NewReader(""), io.Discard, context.Background(), 0)
	if got := len(defaultOptions); got != 3 {
		t.Fatalf("default program options = %d, want 3", got)
	}

	configuredOptions := tuiProgramOptions(strings.NewReader(""), io.Discard, context.Background(), 30)
	if got := len(configuredOptions); got != 4 {
		t.Fatalf("configured program options = %d, want 4", got)
	}
}
