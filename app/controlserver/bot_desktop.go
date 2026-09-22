package controlserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/caelis-labs/caelis/control/bot"
)

func (s *Server) botDesktopRoutes() {
	if s.config.Services.BotWork == nil {
		return
	}
	base := apiPrefix + "/bots/{bot_id}/client/actions"
	s.mux.HandleFunc("GET "+base, s.botDesktopSnapshot)
	s.mux.HandleFunc("GET "+base+"/events", s.botDesktopEvents)
	s.mux.HandleFunc("GET "+base+"/{action_id}", func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.activeBotClient(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.Store.GetDesktopCall(r.Context(), client, r.PathValue("action_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+base+"/{action_id}/claim", func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.activeBotClient(w, r)
		if !ok {
			return
		}
		var empty struct{}
		if !decodeBody(w, r, &empty) {
			return
		}
		out, err := s.config.Services.BotWork.Store.ClaimDesktop(r.Context(), client, r.PathValue("action_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+base+"/{action_id}/result", func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.activeBotClient(w, r)
		if !ok {
			return
		}
		var receipt bot.DesktopReceipt
		if !decodeBody(w, r, &receipt) {
			return
		}
		out, err := s.config.Services.BotWork.Store.CompleteDesktop(r.Context(), client, r.PathValue("action_id"), receipt)
		writeJSONResult(w, out, err)
	})
}
func (s *Server) activeBotClient(w http.ResponseWriter, r *http.Request) (bot.Client, bool) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return bot.Client{}, false
	}
	client, err := s.config.Services.BotWork.Store.ActiveClient(r.Context(), p.ID, p.BotID, p.ClientID)
	if err != nil {
		writeJSONResult(w, nil, err)
		return bot.Client{}, false
	}
	return client, true
}
func (s *Server) botDesktopSnapshot(w http.ResponseWriter, r *http.Request) {
	client, ok := s.activeBotClient(w, r)
	if !ok {
		return
	}
	out, err := s.config.Services.BotWork.Store.SnapshotDesktop(r.Context(), client)
	writeJSONResult(w, out, err)
}

// Desktop SSE carries complete mailbox snapshots. Its content cursor suppresses
// unchanged snapshots after reconnect; a snapshot never authorizes dispatch.
func (s *Server) botDesktopEvents(w http.ResponseWriter, r *http.Request) {
	client, ok := s.activeBotClient(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	cursor := r.Header.Get("Last-Event-ID")
	heartbeat := time.NewTicker(s.config.Heartbeat)
	defer heartbeat.Stop()
	store := s.config.Services.BotWork.Store
	for {
		changed := store.DesktopChanged()
		snapshot, err := store.SnapshotDesktop(r.Context(), client)
		if err != nil {
			return
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			return
		}
		next := snapshot.Cursor
		if next != cursor {
			if _, err := fmt.Fprintf(w, "id: %s\nevent: bot.desktop.snapshot\ndata: %s\n\n", next, data); err != nil {
				return
			}
			flusher.Flush()
			cursor = next
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
