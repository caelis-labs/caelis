package controlserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/gorilla/websocket"
)

const adapterAPIPrefix = "/adapter/v1"

func (s *Server) adapterRoutes() {
	if s.config.AdapterHost == nil {
		return
	}
	s.mux.HandleFunc("GET "+adapterAPIPrefix+"/adapters/{adapter_id}", s.inspectAdapter)
	s.mux.HandleFunc("POST "+adapterAPIPrefix+"/adapters/{adapter_id}/grants", s.issueAdapterGrant)
	s.mux.HandleFunc("GET "+adapterAPIPrefix+"/adapters/{adapter_id}/channel", s.serveAdapterChannel)
}

func (s *Server) inspectAdapter(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePrincipal(w, r); !ok {
		return
	}
	descriptor, err := s.config.AdapterHost.Inspect(r.Context(), r.PathValue("adapter_id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, descriptor)
}

func (s *Server) issueAdapterGrant(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requirePrincipal(w, r)
	if !ok {
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var request controladapterhost.GrantRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid adapter grant request")
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid adapter grant request")
		return
	}
	grant, err := s.config.AdapterHost.IssueGrant(r.Context(), principal.ID, r.PathValue("adapter_id"), request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (s *Server) serveAdapterChannel(w http.ResponseWriter, r *http.Request) {
	if !requestedSubprotocol(r, controladapterhost.ChannelSubprotocol) {
		writeError(w, http.StatusBadRequest, "adapter channel subprotocol is required")
		return
	}
	token, ok := bearerCredential(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "adapter channel grant is required")
		return
	}
	upgrader := websocket.Upgrader{
		Subprotocols:     []string{controladapterhost.ChannelSubprotocol},
		CheckOrigin:      func(*http.Request) bool { return true },
		HandshakeTimeout: 10 * time.Second,
	}
	connection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(maxJSONRequestBytes)
	stream := newWebSocketJSONL(connection)
	err = s.config.AdapterHost.ServeChannel(r.Context(), r.PathValue("adapter_id"), token, stream, stream)
	if err != nil && !errors.Is(err, context.Canceled) {
		_ = connection.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, truncateCloseReason(err.Error())),
			time.Now().Add(time.Second),
		)
	}
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func bearerCredential(value string) (string, bool) {
	scheme, token, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func requestedSubprotocol(request *http.Request, expected string) bool {
	for _, value := range websocket.Subprotocols(request) {
		if value == expected {
			return true
		}
	}
	return false
}

func truncateCloseReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 120 {
		return value[:120]
	}
	return value
}

type webSocketJSONL struct {
	connection  *websocket.Conn
	readMu      sync.Mutex
	writeMu     sync.Mutex
	readBuffer  *bytes.Reader
	writeBuffer bytes.Buffer
}

func newWebSocketJSONL(connection *websocket.Conn) *webSocketJSONL {
	return &webSocketJSONL{connection: connection, readBuffer: bytes.NewReader(nil)}
}

func (s *webSocketJSONL) Read(buffer []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for s.readBuffer.Len() == 0 {
		messageType, payload, err := s.connection.ReadMessage()
		if err != nil {
			return 0, err
		}
		if messageType != websocket.TextMessage {
			return 0, errors.New("adapter channel accepts text frames only")
		}
		payload = bytes.TrimSpace(payload)
		if !json.Valid(payload) || len(payload) == 0 || payload[0] != '{' {
			return 0, errors.New("adapter channel frame must contain one JSON object")
		}
		payload = append(payload, '\n')
		s.readBuffer = bytes.NewReader(payload)
	}
	return s.readBuffer.Read(buffer)
}

func (s *webSocketJSONL) Write(buffer []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := len(buffer)
	_, _ = s.writeBuffer.Write(buffer)
	for {
		line, err := s.writeBuffer.ReadString('\n')
		if errors.Is(err, io.EOF) {
			_, _ = s.writeBuffer.WriteString(line)
			return written, nil
		}
		if err != nil {
			return 0, err
		}
		payload := bytes.TrimSpace([]byte(line))
		if len(payload) == 0 {
			continue
		}
		if !json.Valid(payload) || payload[0] != '{' {
			return 0, fmt.Errorf("adapter channel output is not one JSON object")
		}
		if err := s.connection.WriteMessage(websocket.TextMessage, payload); err != nil {
			return 0, err
		}
	}
}

var _ io.Reader = (*webSocketJSONL)(nil)
var _ io.Writer = (*webSocketJSONL)(nil)
