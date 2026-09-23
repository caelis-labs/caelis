package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func (s *Server) botWorkRoutes() {
	s.botClientRoutes()
	s.botDesktopRoutes()
	s.botReminderRoutes()
	if s.config.Services.BotWork == nil {
		return
	}
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/work", s.listBotWork)
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/work-operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.GetBotWorkOperation(r.Context(), p, r.PathValue("bot_id"), r.PathValue("operation_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/completions", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.ListBotCompletions(r.Context(), p, r.PathValue("bot_id"))
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/work/{work_id}", s.getBotWork)
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/requests/{operation_id}", s.getBotRequest)
	for _, action := range []string{"create", "continue", "steer", "cancel", "acknowledge"} {
		s.mux.HandleFunc("POST "+apiPrefix+"/bots/{bot_id}/work/"+action, s.botWorkCommand(action))
	}
}
func (s *Server) listBotWork(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	out, err := s.config.Services.BotWork.ListBotWork(r.Context(), p, r.PathValue("bot_id"))
	writeJSONResult(w, out, err)
}
func (s *Server) getBotWork(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	out, err := s.config.Services.BotWork.GetBotWork(r.Context(), p, r.PathValue("bot_id"), r.PathValue("work_id"))
	writeJSONResult(w, out, err)
}
func (s *Server) getBotRequest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	out, err := s.config.Services.BotWork.GetBotRequest(r.Context(), p, r.PathValue("bot_id"), r.PathValue("operation_id"))
	writeJSONResult(w, out, err)
}
func (s *Server) botWorkCommand(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.BotWorkRequest
		if !decodeBody(w, r, &req) {
			return
		}
		id := r.PathValue("bot_id")
		sessionID, err := bot.ConversationID(id)
		if err != nil {
			writeJSONResult(w, nil, err)
			return
		}
		if req.BotID != "" && req.BotID != id {
			writeJSONResult(w, nil, appserver.ErrUnauthorized)
			return
		}
		req.BotID = id
		if !applyWriteHeaders(w, r, &req.WriteBase, sessionID) {
			return
		}
		commands := s.config.Services.BotWork.Commands
		var result appserver.CommandResult
		switch action {
		case "acknowledge":
			result, err = commands.AcknowledgeBotCompletion(r.Context(), p, req)
		case "create":
			result, err = commands.CreateBotWork(r.Context(), p, req)
		case "continue":
			result, err = commands.ContinueBotWork(r.Context(), p, req)
		case "steer":
			result, err = commands.SteerBotWork(r.Context(), p, req)
		case "cancel":
			result, err = commands.CancelBotWork(r.Context(), p, req)
		}
		writeCommandResult(w, result, err)
	}
}
