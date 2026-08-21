package controlserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Tasks.List(r.Context(), taskPrincipal(principal), taskstream.ListRequest{
		SessionID: r.PathValue("session_id"),
	})
	writeJSONResult(w, result, err)
}

func (s *Server) watchTaskDirectory(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	directory, ok := s.config.Services.Tasks.(taskstream.DirectoryService)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	result, err := directory.WatchDirectory(r.Context(), taskPrincipal(principal), taskstream.DirectoryWatchRequest{
		SessionID: r.PathValue("session_id"),
	})
	if err != nil {
		writeMappedError(w, err)
		return
	}
	if result.Subscription == nil {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	defer result.Subscription.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(s.config.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case snapshot, open := <-result.Subscription.Snapshots():
			if !open {
				if streamErr := result.Subscription.Err(); streamErr != nil {
					encoded, marshalErr := json.Marshal(taskstream.EncodeStreamError(streamErr))
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", taskstream.StreamErrorEventName, encoded)
						flusher.Flush()
					}
				} else {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", taskstream.StreamDoneEventName)
					flusher.Flush()
				}
				return
			}
			data, marshalErr := marshalTaskDirectorySnapshot(snapshot)
			if marshalErr != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", taskstream.DirectorySnapshotEventName, data)
			flusher.Flush()
		}
	}
}

func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	cursor, ok := resumeCursor(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Tasks.Events(r.Context(), taskPrincipal(principal), taskstream.ReadRequest{
		SessionID:          r.PathValue("session_id"),
		TaskID:             r.PathValue("task_id"),
		Cursor:             cursor,
		ExpectedActivityID: strings.TrimSpace(r.URL.Query().Get("activity_id")),
	})
	if err != nil {
		writeMappedError(w, err)
		return
	}
	data, err := marshalTaskBatch(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	setTaskResumeHeaders(w, result.ResumeMode, result.TransientGap, result.BoundaryCursor)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(data, '\n'))
}

func (s *Server) subscribeTask(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	cursor, ok := resumeCursor(w, r)
	if !ok {
		return
	}
	result, err := s.config.Services.Tasks.Subscribe(r.Context(), taskPrincipal(principal), taskstream.SubscribeRequest{
		SessionID: r.PathValue("session_id"),
		TaskID:    r.PathValue("task_id"),
		Cursor:    cursor,
		Follow:    parseFollowQuery(r),
	})
	if err != nil {
		writeMappedError(w, err)
		return
	}
	if result.Subscription == nil {
		writeError(w, http.StatusServiceUnavailable, "service unavailable")
		return
	}
	defer result.Subscription.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	setTaskResumeHeaders(w, result.ResumeMode, result.TransientGap, result.BoundaryCursor)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := s.config.Heartbeat
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case envelope, open := <-result.Subscription.Events():
			if !open {
				if streamErr := result.Subscription.Err(); streamErr != nil {
					encoded, marshalErr := json.Marshal(taskstream.EncodeStreamError(streamErr))
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", taskstream.StreamErrorEventName, encoded)
						flusher.Flush()
					}
				} else {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", taskstream.StreamDoneEventName)
					flusher.Flush()
				}
				return
			}
			data, marshalErr := wirev1.MarshalEnvelope(envelope)
			if marshalErr != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", envelope.Cursor, data)
			flusher.Flush()
		}
	}
}

func taskPrincipal(principal appserver.Principal) taskstream.Principal {
	return taskstream.Principal{
		ID:    principal.ID,
		Roles: append([]string(nil), principal.Roles...),
	}
}

func parseFollowQuery(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("follow"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type taskBatchWire struct {
	Events         []json.RawMessage     `json:"events,omitempty"`
	ActivityID     string                `json:"activity_id,omitempty"`
	ResumeMode     taskstream.ResumeMode `json:"resume_mode"`
	TransientGap   bool                  `json:"transient_gap,omitempty"`
	BoundaryCursor string                `json:"boundary_cursor,omitempty"`
}

type taskDirectorySnapshotWire struct {
	Revision string                      `json:"revision"`
	Tasks    []taskstream.TaskDescriptor `json:"tasks,omitempty"`
}

func marshalTaskDirectorySnapshot(snapshot taskstream.DirectorySnapshot) ([]byte, error) {
	return wirev1.Marshal(taskDirectorySnapshotWire{
		Revision: strconv.FormatUint(snapshot.Revision, 10),
		Tasks:    snapshot.Tasks,
	})
}

func marshalTaskBatch(batch taskstream.Batch) ([]byte, error) {
	wire := taskBatchWire{
		Events:         make([]json.RawMessage, 0, len(batch.Events)),
		ActivityID:     batch.ActivityID,
		ResumeMode:     batch.ResumeMode,
		TransientGap:   batch.TransientGap,
		BoundaryCursor: batch.BoundaryCursor,
	}
	for _, envelope := range batch.Events {
		raw, err := wirev1.MarshalEnvelope(envelope)
		if err != nil {
			return nil, err
		}
		wire.Events = append(wire.Events, raw)
	}
	return json.Marshal(wire)
}

func setTaskResumeHeaders(
	w http.ResponseWriter,
	mode taskstream.ResumeMode,
	transientGap bool,
	boundaryCursor string,
) {
	w.Header().Set(resumeModeHeader, string(mode))
	w.Header().Set(transientGapHeader, strconv.FormatBool(transientGap))
	if boundaryCursor != "" {
		w.Header().Set(boundaryCursorHeader, boundaryCursor)
	}
}
