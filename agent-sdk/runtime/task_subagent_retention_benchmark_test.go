package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

// These benchmarks exercise only the production retention and append paths.
// Workloads are built outside the timed region so results measure ingest,
// coalescing, exact-ring maintenance, and eviction rather than fixture setup.
func BenchmarkSubagentSemanticRetentionScaling(b *testing.B) {
	for _, count := range []int{1_024, 4_096, 16_384} {
		frames := subagentBenchmarkReasoningFrames(count, 64)
		b.Run("frames="+strconv.Itoa(count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(subagentBenchmarkFrameTextBytes(frames))
			for range b.N {
				retention := subagentSemanticRetention{}
				for index, frame := range frames {
					retention.observe(frame, int64(index+1))
				}
				subagentRetentionBenchmarkSink = retention.bytes + len(retention.units)
			}
		})
	}
}

func BenchmarkSubagentAppendStreamFrameScaling(b *testing.B) {
	for _, count := range []int{1_024, 4_096, 16_384} {
		frames := subagentBenchmarkReasoningFrames(count, 64)
		b.Run("frames="+strconv.Itoa(count), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(subagentBenchmarkFrameTextBytes(frames))
			for range b.N {
				task := newSubagentRetentionBenchmarkTask()
				for _, frame := range frames {
					task.appendStreamFrameLocked(frame)
				}
				subagentRetentionBenchmarkSink = task.streamBytes + task.semanticRetention.bytes +
					len(task.streamFrames) + len(task.semanticRetention.units)
			}
		})
	}
}

func BenchmarkSubagentAppendStreamFrameMultiTurn(b *testing.B) {
	for _, turns := range []int{64, 256, 1_024} {
		b.Run("turns="+strconv.Itoa(turns), func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				task := newSubagentRetentionBenchmarkTask()
				for turn := 1; turn <= turns; turn++ {
					turnID := subagentTurnID(task.ref.TaskID, task.turnSeq)
					for chunk := 0; chunk < 15; chunk++ {
						task.appendStreamFrameLocked(subagentBenchmarkNarrativeFrame(
							turnID,
							fmt.Sprintf("thought-%d", turn),
							strings.Repeat("r", 64),
						))
					}
					task.applyResult(delegation.Result{
						TaskID: task.ref.TaskID,
						State:  delegation.StateCompleted,
						Result: fmt.Sprintf("exact final for turn %d", turn),
					})
					task.ensureTerminalStreamFrameLocked()
					if turn < turns {
						beginObservedActivityForTest(task)
					}
				}
				subagentRetentionBenchmarkSink = task.streamBytes + task.semanticRetention.bytes +
					len(task.streamFrames) + len(task.semanticRetention.units) + len(task.latestFinalText)
			}
		})
	}
}

func BenchmarkSubagentSemanticRetentionOversizedUnit(b *testing.B) {
	frame := subagentBenchmarkNarrativeFrame("task-benchmark:1", "thought-oversized", strings.Repeat("x", 2*1024*1024))
	b.ReportAllocs()
	b.SetBytes(int64(len(session.EventText(frame.Event))))
	for range b.N {
		retention := subagentSemanticRetention{}
		retention.observe(frame, 1)
		subagentRetentionBenchmarkSink = retention.bytes + len(retention.units)
	}
}

func newSubagentRetentionBenchmarkTask() *subagentTask {
	return &subagentTask{
		ref:        taskapi.Ref{TaskID: "task-benchmark", SessionID: "session-benchmark"},
		sessionRef: session.SessionRef{SessionID: "session-benchmark"},
		state:      taskapi.StateRunning,
		running:    true,
		turnSeq:    1,
	}
}

func subagentBenchmarkReasoningFrames(count int, chunkBytes int) []stream.Frame {
	frames := make([]stream.Frame, count)
	text := strings.Repeat("r", chunkBytes)
	for index := range frames {
		frames[index] = subagentBenchmarkNarrativeFrame("task-benchmark:1", "thought-1", text)
		frames[index].Event.ID = fmt.Sprintf("thought-chunk-%d", index)
	}
	return frames
}

func subagentBenchmarkNarrativeFrame(turnID string, messageID string, text string) stream.Frame {
	return stream.Frame{Running: true, Event: &session.Event{
		MessageID: messageID,
		Type:      session.EventTypeAssistant,
		Text:      text,
		Scope:     &session.EventScope{TurnID: turnID},
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     messageID,
			Content:       session.ProtocolTextContent(text),
		}},
	}}
}

func subagentBenchmarkFrameTextBytes(frames []stream.Frame) int64 {
	var total int64
	for _, frame := range frames {
		total += int64(len(session.EventText(frame.Event)))
	}
	return total
}

var subagentRetentionBenchmarkSink int
