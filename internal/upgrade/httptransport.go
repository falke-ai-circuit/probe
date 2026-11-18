package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPTransport talks to the server's v1 agent endpoints with an operator
// bearer token. It is the production Transport for the caller.
type HTTPTransport struct {
	BaseURL string // e.g. https://127.0.0.1:8443
	AgentID string
	Token   string
	Client  *http.Client
}

// NewHTTPTransport builds a transport with a client bounded per call.
func NewHTTPTransport(baseURL, agentID, token string, timeout time.Duration) *HTTPTransport {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &HTTPTransport{
		BaseURL: strings.TrimRight(baseURL, "/"),
		AgentID: agentID,
		Token:   token,
		Client:  &http.Client{Timeout: timeout},
	}
}

func (h *HTTPTransport) call(ctx context.Context, method, path string, body any, out any) error {
	if h.Client == nil {
		h.Client = &http.Client{Timeout: 120 * time.Second}
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	endpoint := h.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode %s: %w (status %d)", path, err, resp.StatusCode)
	}
	if !envelope.OK {
		code := "INTERNAL_ERROR"
		message := strings.TrimSpace(string(data))
		if envelope.Error != nil {
			code = envelope.Error.Code
			message = envelope.Error.Message
		}
		return normalizeAgentError(code, message)
	}
	if out != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode data from %s: %w", path, err)
		}
	}
	return nil
}

func (h *HTTPTransport) agentPath(suffix string) string {
	return "/api/v1/agents/" + url.PathEscape(h.AgentID) + suffix
}

// Exec runs POST /api/v1/agents/{id}/exec.
func (h *HTTPTransport) Exec(ctx context.Context, req ExecRequest) (ExecResult, error) {
	var res ExecResult
	err := h.call(ctx, http.MethodPost, h.agentPath("/exec"), req, &res)
	return res, err
}

// FsWrite runs POST /api/v1/agents/{id}/fs-write with base64 data.
func (h *HTTPTransport) FsWrite(ctx context.Context, req WriteRequest) (WriteResult, error) {
	wire := struct {
		Path   string `json:"path"`
		Data   string `json:"data"`
		Offset int    `json:"offset,omitempty"`
		Mode   string `json:"mode,omitempty"`
	}{
		Path:   req.Path,
		Data:   encodeBase64(req.Data),
		Offset: req.Offset,
		Mode:   req.Mode,
	}
	var res WriteResult
	err := h.call(ctx, http.MethodPost, h.agentPath("/fs-write"), wire, &res)
	return res, err
}

// FsHash runs POST /api/v1/agents/{id}/fs-hash.
func (h *HTTPTransport) FsHash(ctx context.Context, path string) (HashResult, error) {
	var res HashResult
	err := h.call(ctx, http.MethodPost, h.agentPath("/fs-hash"), map[string]string{"path": path}, &res)
	return res, err
}

// FsStat runs POST /api/v1/agents/{id}/fs-stat.
func (h *HTTPTransport) FsStat(ctx context.Context, path string) (StatResult, error) {
	var res StatResult
	err := h.call(ctx, http.MethodPost, h.agentPath("/fs-stat"), map[string]string{"path": path}, &res)
	return res, err
}

// FsRead runs POST /api/v1/agents/{id}/fs-read and decodes the base64 body.
func (h *HTTPTransport) FsRead(ctx context.Context, req ReadRequest) (ReadResult, error) {
	wire := struct {
		Path   string `json:"path"`
		Offset int    `json:"offset,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}{Path: req.Path, Offset: req.Offset, Limit: req.Limit}
	var res struct {
		Data     string `json:"data"`
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
	}
	if err := h.call(ctx, http.MethodPost, h.agentPath("/fs-read"), wire, &res); err != nil {
		return ReadResult{}, err
	}
	data, err := decodeBase64(res.Data)
	if err != nil {
		return ReadResult{}, fmt.Errorf("decode fs-read body: %w", err)
	}
	return ReadResult{Data: data, Size: res.Size}, nil
}
