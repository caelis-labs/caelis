package controlserver

import (
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

func (s *Server) botClientRoutes() {
	if s.config.Services.BotWork == nil {
		return
	}
	s.mux.HandleFunc("GET "+apiPrefix+"/bots/{bot_id}/client", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		out, err := s.config.Services.BotWork.Store.ClientState(r.Context(), p.ID, p.BotID, p.ClientID)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/bots/{bot_id}/clients/register", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.RegisterBotClientRequest
		if !decodeBody(w, r, &req) || !applyHostWriteHeaders(w, r, &req.WriteBase) {
			return
		}
		if req.BotID != "" && req.BotID != r.PathValue("bot_id") {
			writeJSONResult(w, nil, appserver.ErrUnauthorized)
			return
		}
		req.BotID = r.PathValue("bot_id")
		out, err := s.config.Services.BotWork.RegisterBotClient(r.Context(), p, req)
		writeJSONResult(w, out, err)
	})
	s.mux.HandleFunc("POST "+apiPrefix+"/bots/{bot_id}/client/exit", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.requirePrincipal(w, r)
		if !ok {
			return
		}
		var req appserver.BotClientExitRequest
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
		out, err := s.config.Services.BotWork.Commands.ExitBotClient(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	for _, mode := range []string{"activate", "renew"} {
		s.mux.HandleFunc("POST "+apiPrefix+"/bots/{bot_id}/client/"+mode, func(w http.ResponseWriter, r *http.Request) {
			p, ok := s.requirePrincipal(w, r)
			if !ok {
				return
			}
			if p.ClientID == "" {
				writeJSONResult(w, nil, appserver.ErrUnauthorized)
				return
			}
			var empty struct{}
			if !decodeBody(w, r, &empty) {
				return
			}
			_, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			client, err := s.config.Services.BotWork.Store.AuthenticateClient(r.Context(), token)
			if err == nil && mode == "renew" {
				client, err = s.config.Services.BotWork.Store.ActiveClient(r.Context(), p.ID, p.BotID, p.ClientID)
			}
			if err == nil {
				client, err = s.config.Services.BotWork.Store.ActivateClient(r.Context(), client)
			}
			if err == nil && s.config.Services.BotWork.Wake != nil {
				s.config.Services.BotWork.Wake(r.Context(), p.ID, p.BotID)
			}
			writeJSONResult(w, client, err)
		})
	}
}

func (s *Server) botClientPrincipal(r *http.Request, token string) (appserver.Principal, error) {
	service := s.config.Services.BotWork
	if service == nil || service.Store == nil {
		return appserver.Principal{}, appserver.ErrUnauthorized
	}
	client, err := service.Store.AuthenticateClient(r.Context(), token)
	if err != nil {
		return appserver.Principal{}, err
	}
	p := appserver.Principal{ID: client.PrincipalID, ClientID: client.ID, BotID: client.BotID}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	if (r.Method == http.MethodPost && (path == "/bots/"+client.BotID+"/client/activate" || path == "/bots/"+client.BotID+"/client/exit")) || (r.Method == http.MethodGet && path == "/bots/"+client.BotID+"/client") {
		return p, nil
	}
	if _, err := service.Store.ActiveClient(r.Context(), p.ID, p.BotID, p.ClientID); err != nil {
		return appserver.Principal{}, err
	}
	switch {
	case path == "/initialize", path == "/bots", path == "/bots/"+client.BotID:
		return p, nil
	case strings.HasPrefix(path, "/bots/"+client.BotID+"/"):
		if strings.Contains(path, "/clients/register") {
			return appserver.Principal{}, appserver.ErrUnauthorized
		}
		return p, nil
	case strings.HasPrefix(path, "/sessions/"):
		rest := strings.TrimPrefix(path, "/sessions/")
		id, _, ok := strings.Cut(rest, "/")
		if !ok {
			return appserver.Principal{}, appserver.ErrUnauthorized
		}
		if err := service.AuthorizeBotSession(r.Context(), p, id); err != nil {
			return appserver.Principal{}, err
		}
		return p, nil
	default:
		return appserver.Principal{}, errorcode.New(errorcode.PermissionDenied, "controlserver: route is outside the Bot client scope")
	}
}
