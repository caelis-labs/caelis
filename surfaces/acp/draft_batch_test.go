package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
)

func TestDraftProbeRejectsWholeBatchBeforeInitialize(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	defer left.Close()
	done := make(chan error, 1)
	go func() {
		done <- ServeDraftStdio(ctx, DraftSchemaTag, acpsdk.Implementation{Name: "probe", Version: "test"}, right, right)
	}()
	t.Cleanup(func() { cancel(); _ = left.Close(); _ = right.Close(); <-done })
	if err := left.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(left)
	for _, batch := range []string{`[]`, `[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2,"info":{"name":"peer","version":"test"}}}]`} {
		if _, err := fmt.Fprintln(left, batch); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		var response struct {
			ID    json.RawMessage      `json:"id"`
			Error *acpsdk.RequestError `json:"error"`
		}
		if err != nil || json.Unmarshal(line, &response) != nil || response.Error == nil || response.Error.Code != -32600 || string(response.ID) != "null" {
			t.Fatalf("whole batch rejection: %s, %v", line, err)
		}
	}
	if _, err := fmt.Fprintln(left, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":2,"info":{"name":"peer","version":"test"}}}`); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	var response struct {
		Result struct {
			Meta map[string]string `json:"_meta"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(line, &response) != nil || response.Result.Meta[DraftSchemaMetaKey] != DraftSchemaTag {
		t.Fatalf("single initialize after rejected batch: %s, %v", line, err)
	}
}
