package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/collaboration"
)

// Collaboration binds a participant-only credential to the mailbox endpoint.
// It cannot be used for ordinary AppServer commands.
func Collaboration(endpoint, token string) collaboration.Invoke {
	return func(ctx context.Context, input collaboration.Request) (json.RawMessage, error) {
		body, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+wirev1.APIPrefix+"/collaboration", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()
		out, err := io.ReadAll(io.LimitReader(response.Body, collaboration.MaxResponseBytes+1))
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("collaboration: HTTP %d: %s", response.StatusCode, out)
		}
		if len(out) > collaboration.MaxResponseBytes {
			return nil, fmt.Errorf("collaboration: response exceeds byte budget")
		}
		if !json.Valid(out) {
			return nil, fmt.Errorf("collaboration: invalid response")
		}
		return out, nil
	}
}
