package tuiapp

import "testing"

func TestLogStreamBufferEmitsCompleteLinesAndKeepsTail(t *testing.T) {
	var b logStreamBuffer

	lines := b.Append("one\ntwo")
	if len(lines) != 1 || lines[0] != "one" {
		t.Fatalf("lines = %#v, want [one]", lines)
	}
	if tail := b.Tail(); tail != "two" {
		t.Fatalf("tail = %q, want two", tail)
	}

	lines = b.Append("\n")
	if len(lines) != 1 || lines[0] != "two" || b.Tail() != "" {
		t.Fatalf("flush lines=%#v tail=%q", lines, b.Tail())
	}
}

func TestPartialLogTailDoesNotForceDirtyBlockFullSync(t *testing.T) {
	m := newPerfTestModel()
	block := NewAssistantBlock("assistant")
	block.Raw = "initial assistant text"
	m.doc.Append(block)
	m.markViewportStructureDirty()
	m.syncViewportContent()

	_, _ = m.handleLogChunk("partial")
	m.syncViewportContent()
	fullBefore := m.diag.ViewportFullSyncs

	block.Raw += " plus dirty tail"
	m.markViewportBlockDirty(block.BlockID())
	m.syncViewportContent()

	if got := m.diag.ViewportFullSyncs; got != fullBefore {
		t.Fatalf("partial log tail forced full syncs = %d, want %d", got, fullBefore)
	}
}
