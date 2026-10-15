package httpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"io"
	"net/http"
	"net/url"
)

func verifyResourceContent(sessionID, id string, out appserver.SessionResourceContent) (appserver.SessionResourceContent, error) {
	digest := sha256.Sum256(out.Data)
	if out.Resource.ID != id || out.Resource.SessionID != sessionID || out.Resource.Size != int64(len(out.Data)) || len(out.Data) > sessionprofile.MaxResourceBytes || out.Resource.SHA256 != hex.EncodeToString(digest[:]) {
		return appserver.SessionResourceContent{}, errors.New("application resource integrity mismatch")
	}
	return out, nil
}
func sessionJSON[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any, operation string, resource bool) (T, error) {
	var out T
	headers := http.Header{}
	if operation != "" {
		headers.Set("Idempotency-Key", operation)
	}
	response, err := c.do(ctx, method, path, query, body, headers)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	limit := int64(maxRemoteResponseBody)
	if resource {
		limit = 4*((sessionprofile.MaxResourceBytes+2)/3) + 64*1024
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return out, err
	}
	if int64(len(raw)) > limit {
		return out, errors.New("application response exceeds size limit")
	}
	err = wirev1.Unmarshal(raw, &out)
	return out, err
}
