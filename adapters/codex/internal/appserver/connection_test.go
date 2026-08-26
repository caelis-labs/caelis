package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestConnectionMultiplexesResponsesNotificationsAndRequests(t *testing.T) {
	serverToClientReader, serverToClientWriter := io.Pipe()
	clientToServerReader, clientToServerWriter := io.Pipe()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()

	notifications := make(chan Notification, 1)
	requests := make(chan Request, 1)
	connection, err := NewConnection(serverToClientReader, clientToServerWriter, func(notification Notification) {
		notifications <- notification
	}, func(_ context.Context, request Request) (any, error) {
		requests <- request
		return map[string]any{"decision": "accept"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(clientToServerReader)
		var request message
		line, err := reader.ReadBytes('\n')
		if err != nil {
			serverErr <- err
			return
		}
		if err := json.Unmarshal(line, &request); err != nil {
			serverErr <- err
			return
		}
		if request.Method != "thread/read" {
			serverErr <- &unexpectedMethodError{got: request.Method, want: "thread/read"}
			return
		}
		if err := writeTestMessage(serverToClientWriter, message{ID: request.ID, Result: json.RawMessage(`{"thread":{"id":"thread-1"}}`)}); err != nil {
			serverErr <- err
			return
		}
		if err := writeTestMessage(serverToClientWriter, message{Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread-1"}`)}); err != nil {
			serverErr <- err
			return
		}
		if err := writeTestMessage(serverToClientWriter, message{ID: json.RawMessage("99"), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread-1"}`)}); err != nil {
			serverErr <- err
			return
		}
		line, err = reader.ReadBytes('\n')
		if err != nil {
			serverErr <- err
			return
		}
		var response message
		if err := json.Unmarshal(line, &response); err != nil {
			serverErr <- err
			return
		}
		var result struct {
			Decision string `json:"decision"`
		}
		if string(response.ID) != "99" || json.Unmarshal(response.Result, &result) != nil || result.Decision != "accept" {
			serverErr <- &unexpectedResponseError{}
			return
		}
		serverErr <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := connection.Request(ctx, "thread/read", map[string]any{"threadId": "thread-1"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Thread.ID != "thread-1" {
		t.Fatalf("thread id = %q", result.Thread.ID)
	}
	select {
	case notification := <-notifications:
		if notification.Sequence != 1 || notification.Method != "turn/started" {
			t.Fatalf("notification = %#v", notification)
		}
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	select {
	case request := <-requests:
		if request.Method != "item/commandExecution/requestApproval" {
			t.Fatalf("request = %#v", request)
		}
	case <-ctx.Done():
		t.Fatal(context.Cause(ctx))
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func writeTestMessage(writer io.Writer, value message) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

type unexpectedMethodError struct{ got, want string }

func (e *unexpectedMethodError) Error() string { return "method = " + e.got + ", want " + e.want }

type unexpectedResponseError struct{}

func (*unexpectedResponseError) Error() string { return "unexpected app-server response" }
