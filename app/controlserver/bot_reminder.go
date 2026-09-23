package controlserver

import (
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"net/http"
)

func (s *Server) botReminderRoutes() {
	if s.config.Services.BotWork == nil {
		return
	}
	base := apiPrefix + "/bots/{bot_id}/client/reminders"
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/client/reminder-occurrences", func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.activeBotClient(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.Store.ReminderOccurrences(r.Context(), client)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		client, ok := s.activeBotClient(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.Store.Reminders(r.Context(), client)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+base+"/fire", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.BotReminderRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if req.BotID != "" && req.BotID != r.PathValue("bot_id") {
			writeJSONResult(w, nil, appserver.ErrUnauthorized)
			return
		}
		req.BotID = r.PathValue("bot_id")
		id, err := bot.ConversationID(req.BotID)
		if err != nil {
			writeJSONResult(w, nil, err)
			return
		}
		if !applyWriteHeaders(w, r, &req.WriteBase, id) {
			return
		}
		out, err := s.config.Services.BotWork.Commands.FireBotReminder(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
}
