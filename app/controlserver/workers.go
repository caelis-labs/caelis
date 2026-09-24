package controlserver

import (
	"net/http"

	"github.com/caelis-labs/caelis/control/appserver"
)

func (s *Server) workerRoutes() {
	service := s.config.Services.Applications
	s.mux.HandleFunc("POST "+apiPrefix+"/application/workers", func(w http.ResponseWriter, r *http.Request) {
		p, _, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		var req appserver.CreateWorkerRequest
		if !decodeBody(w, r, &req) || !applyHostWriteHeaders(w, r, &req.WriteBase) {
			return
		}
		out, err := service.CreateWorker(r.Context(), p, req)
		writeCommandResult(w, out, err)
	})
	s.mux.HandleFunc("GET "+apiPrefix+"/application/workers", func(w http.ResponseWriter, r *http.Request) {
		_, scope, ok := s.requireApplication(w, r)
		if !ok {
			return
		}
		out, err := service.Store().Workers(r.Context(), scope)
		writeJSONResult(w, out, err)
	})
}
