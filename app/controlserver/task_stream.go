package controlserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
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
					encoded, marshalErr := json.Marshal(wirev1.EncodeTaskStreamError(streamErr))
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wirev1.TaskStreamErrorEventName, encoded)
						flusher.Flush()
					}
				} else {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", wirev1.TaskStreamDoneEventName)
					flusher.Flush()
				}
				return
			}
			data, marshalErr := marshalTaskDirectorySnapshot(snapshot)
			if marshalErr != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wirev1.TaskDirectorySnapshotEventName, data)
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
		case delivery, open := <-result.Subscription.Deliveries():
			if !open {
				if streamErr := result.Subscription.Err(); streamErr != nil {
					encoded, marshalErr := json.Marshal(wirev1.EncodeTaskStreamError(streamErr))
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wirev1.TaskStreamErrorEventName, encoded)
						flusher.Flush()
					}
				} else {
					_, _ = fmt.Fprintf(w, "event: %s\ndata: {}\n\n", wirev1.TaskStreamDoneEventName)
					flusher.Flush()
				}
				return
			}
			data, marshalErr := marshalTaskDelivery(delivery)
			if marshalErr != nil {
				return
			}
			if delivery.NextCursor != "" {
				_, _ = fmt.Fprintf(w, "id: %s\n", delivery.NextCursor)
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", wirev1.TaskStreamDeliveryEventName, data)
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

func marshalTaskBatch(batch taskstream.ReadResult) ([]byte, error) {
	wire := wirev1.TaskStreamReadResult{
		Deliveries: make([]wirev1.TaskStreamDelivery, 0, len(batch.Deliveries)),
		ActivityID: batch.ActivityID,
	}
	for _, delivery := range batch.Deliveries {
		encoded, err := taskDeliveryWire(delivery)
		if err != nil {
			return nil, err
		}
		wire.Deliveries = append(wire.Deliveries, encoded)
	}
	return json.Marshal(wire)
}

func marshalTaskDelivery(delivery taskstream.Delivery) ([]byte, error) {
	wire, err := taskDeliveryWire(delivery)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wire)
}

func taskDeliveryWire(delivery taskstream.Delivery) (wirev1.TaskStreamDelivery, error) {
	wire := wirev1.TaskStreamDelivery{
		Kind: string(delivery.Kind), Source: string(delivery.Source),
		SnapshotID: delivery.SnapshotID, Page: delivery.Page,
		NextCursor: delivery.NextCursor, ActivityID: delivery.ActivityID,
		Events: make([]json.RawMessage, 0, len(delivery.Events)),
	}
	for _, envelope := range delivery.Events {
		raw, err := wirev1.MarshalEnvelope(envelope)
		if err != nil {
			return wirev1.TaskStreamDelivery{}, err
		}
		wire.Events = append(wire.Events, raw)
	}
	return wire, nil
}
