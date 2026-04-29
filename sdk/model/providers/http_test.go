package providers

import (
	"net/http"
	"net/http/httptest"
)

type providerTestServer struct {
	URL     string
	handler http.Handler
}

func newProviderTestServer(handler http.Handler) *providerTestServer {
	return &providerTestServer{
		URL:     "http://provider.test",
		handler: handler,
	}
}

func (s *providerTestServer) Client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
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
			resp := recorder.Result()
			resp.Request = req
			return resp, nil
		}
	})}
}

func (s *providerTestServer) Close() {}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
