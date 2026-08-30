// Package adapterhostclient implements the private Caelis Adapter Channel
// client used by the lightweight ACP stdio proxy.
package adapterhostclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	controladapterhost "github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/gorilla/websocket"
)

const maxFrameBytes = 64 << 20

// Config addresses one authenticated Caelis Host.
type Config struct {
	BaseURL     string
	BearerToken string
	HTTPClient  *http.Client
}

// ChannelGrantFile is the one-use launch material passed to an internal stdio
// proxy. The file contains no general Control bearer credential.
type ChannelGrantFile struct {
	SchemaVersion int    `json:"schema_version"`
	Endpoint      string `json:"endpoint"`
	AdapterID     string `json:"adapter_id"`
	Token         string `json:"token"`
}

const channelGrantFileSchemaVersion = 1

// WriteChannelGrantFile atomically creates a private one-use grant file.
func WriteChannelGrantFile(dir string, grant ChannelGrantFile) (string, error) {
	grant.SchemaVersion = channelGrantFileSchemaVersion
	grant.Endpoint = strings.TrimSpace(grant.Endpoint)
	grant.AdapterID = strings.ToLower(strings.TrimSpace(grant.AdapterID))
	grant.Token = strings.TrimSpace(grant.Token)
	if grant.Endpoint == "" || grant.AdapterID == "" || grant.Token == "" {
		return "", errors.New("adapterhostclient: complete channel grant is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".adapter-grant-*.json")
	if err != nil {
		return "", err
	}
	path := file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if err := secureChannelGrantFile(file); err != nil {
		return "", err
	}
	if err := json.NewEncoder(file).Encode(grant); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	cleanup = false
	return path, nil
}

// ConsumeChannelGrantFile reads and removes one private one-use grant file.
func ConsumeChannelGrantFile(path string) (ChannelGrantFile, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return ChannelGrantFile{}, errors.New("adapterhostclient: absolute channel grant file is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return ChannelGrantFile{}, err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(path)
	}()
	info, err := file.Stat()
	if err != nil {
		return ChannelGrantFile{}, err
	}
	if !info.Mode().IsRegular() {
		return ChannelGrantFile{}, errors.New("adapterhostclient: channel grant path must be a regular file")
	}
	if err := validateChannelGrantFileSecurity(file, info); err != nil {
		return ChannelGrantFile{}, err
	}
	var grant ChannelGrantFile
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grant); err != nil {
		return ChannelGrantFile{}, err
	}
	if grant.SchemaVersion != channelGrantFileSchemaVersion || strings.TrimSpace(grant.Endpoint) == "" || strings.TrimSpace(grant.AdapterID) == "" || strings.TrimSpace(grant.Token) == "" {
		return ChannelGrantFile{}, errors.New("adapterhostclient: invalid channel grant file")
	}
	return grant, nil
}

// Client issues scoped grants and opens private adapter channels.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New constructs a private Adapter Host client.
func New(config Config) (*Client, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("adapterhostclient: valid http(s) Host URL is required")
	}
	if strings.TrimSpace(config.BearerToken) == "" {
		return nil, errors.New("adapterhostclient: Host bearer token is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: baseURL, token: strings.TrimSpace(config.BearerToken), httpClient: client}, nil
}

// NewChannel constructs a client that may only open an already-authorized
// channel; it carries no general Host bearer credential.
func NewChannel(baseURL string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("adapterhostclient: valid http(s) Host URL is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

// Inspect obtains backend availability without starting it.
func (c *Client) Inspect(ctx context.Context, adapterID string) (controladapterhost.Descriptor, error) {
	var descriptor controladapterhost.Descriptor
	if err := c.doJSON(ctx, http.MethodGet, c.adapterPath(adapterID), nil, &descriptor); err != nil {
		return descriptor, err
	}
	return descriptor, nil
}

// IssueGrant obtains one single-use adapter channel credential.
func (c *Client) IssueGrant(ctx context.Context, adapterID string, request controladapterhost.GrantRequest) (controladapterhost.Grant, error) {
	var grant controladapterhost.Grant
	if err := c.doJSON(ctx, http.MethodPost, c.adapterPath(adapterID)+"/grants", request, &grant); err != nil {
		return grant, err
	}
	return grant, nil
}

// Proxy opens a channel with an already-issued grant and copies ACP JSONL
// without changing JSON payloads.
func (c *Client) Proxy(ctx context.Context, adapterID, grant string, input io.Reader, output io.Writer) error {
	if input == nil || output == nil {
		return errors.New("adapterhostclient: proxy input and output are required")
	}
	if strings.TrimSpace(grant) == "" {
		return errors.New("adapterhostclient: adapter channel grant is required")
	}
	channelURL, err := url.Parse(c.baseURL + c.adapterPath(adapterID) + "/channel")
	if err != nil {
		return err
	}
	if channelURL.Scheme == "https" {
		channelURL.Scheme = "wss"
	} else {
		channelURL.Scheme = "ws"
	}
	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = []string{controladapterhost.ChannelSubprotocol}
	header := http.Header{"Authorization": []string{"Bearer " + strings.TrimSpace(grant)}}
	connection, response, err := dialer.DialContext(ctx, channelURL.String(), header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			return fmt.Errorf("adapterhostclient: open channel: %s: %s", response.Status, strings.TrimSpace(string(body)))
		}
		return fmt.Errorf("adapterhostclient: open channel: %w", err)
	}
	defer connection.Close()
	if connection.Subprotocol() != controladapterhost.ChannelSubprotocol {
		return errors.New("adapterhostclient: Host did not negotiate the adapter channel subprotocol")
	}
	connection.SetReadLimit(maxFrameBytes)
	errCh := make(chan error, 2)
	go func() { errCh <- copyJSONLToWebSocket(connection, input) }()
	go func() { errCh <- copyWebSocketToJSONL(connection, output) }()
	select {
	case <-ctx.Done():
		_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "cancelled"), time.Now().Add(time.Second))
		return context.Cause(ctx)
	case err := <-errCh:
		if err == nil {
			_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "eof"), time.Now().Add(time.Second))
			second := <-errCh
			if isNormalWebSocketClose(second) {
				return nil
			}
			return second
		}
		if isNormalWebSocketClose(err) {
			return nil
		}
		return err
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("adapterhostclient: Host returned %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(responseBody); err != nil {
		return err
	}
	return nil
}

func (c *Client) adapterPath(adapterID string) string {
	return "/adapter/v1/adapters/" + url.PathEscape(strings.ToLower(strings.TrimSpace(adapterID)))
}

func copyJSONLToWebSocket(connection *websocket.Conn, input io.Reader) error {
	reader := bufio.NewReaderSize(input, 64<<10)
	for {
		line, err := readBoundedLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) || line[0] != '{' {
			return errors.New("adapterhostclient: ACP stdin line must contain one JSON object")
		}
		if err := connection.WriteMessage(websocket.TextMessage, line); err != nil {
			return err
		}
	}
}

func copyWebSocketToJSONL(connection *websocket.Conn, output io.Writer) error {
	for {
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			return err
		}
		if messageType != websocket.TextMessage {
			return errors.New("adapterhostclient: Host sent a non-text adapter frame")
		}
		payload = bytes.TrimSpace(payload)
		if !json.Valid(payload) || len(payload) == 0 || payload[0] != '{' {
			return errors.New("adapterhostclient: Host adapter frame is not one JSON object")
		}
		_, writeErr := output.Write(append(payload, '\n'))
		if writeErr != nil {
			return writeErr
		}
	}
}

func readBoundedLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, more, err := reader.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(line)+len(part) > maxFrameBytes {
			return nil, errors.New("adapterhostclient: ACP message exceeds 64 MiB")
		}
		line = append(line, part...)
		if !more {
			return line, nil
		}
	}
}

func isNormalWebSocketClose(err error) bool {
	return err == nil || websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway)
}
