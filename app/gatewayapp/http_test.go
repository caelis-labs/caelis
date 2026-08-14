package gatewayapp

import (
	"net/http"
	"net/http/httptest"
)

type gatewayTestHTTPServer struct {
	URL     string
	handler http.Handler
}

func newGatewayTestHTTPServer(handler http.Handler) *gatewayTestHTTPServer {
	return &gatewayTestHTTPServer{
		URL:     "http://gateway.test",
		handler: handler,
	}
}

func (s *gatewayTestHTTPServer) Client() *http.Client {
	return &http.Client{Transport: gatewayRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.handler.ServeHTTP(recorder, req)
		}()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-done:
			response := recorder.Result()
			response.Request = req
			return response, nil
		}
	})}
}

func (*gatewayTestHTTPServer) Close() {}

type gatewayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gatewayRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
