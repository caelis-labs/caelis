package testenv

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestHTTPServerStreamsAndCancelsWithoutHostSocket(t *testing.T) {
	cancelled := make(chan struct{})
	server := NewHTTPServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			t.Error("in-memory response writer is not flushable")
			return
		}
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(writer, "first")
		flusher.Flush()
		<-request.Context().Done()
		close(cancelled)
	}))

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		server.URL+"/stream",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "first\n" {
		t.Fatalf("streamed line = %q", line)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("closing response body did not cancel the server request")
	}
}
