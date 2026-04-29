package providers

import "net/http"

func coalesceHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{}
}
