// Package mcpserver serves Brokoli over the Model Context Protocol
// (ADR-046).
//
// Every tool is a call to Brokoli's own REST API, made through a Caller.
// On the server (POST /api/mcp) the Caller sends the request through the
// server's own router, in process, with the MCP request's credentials, so
// the route's middleware and permission checks decide exactly as they do
// for any API request. In `brokoli mcp` the Caller is an HTTP client for a
// Brokoli server. Either way there is one implementation of each tool and
// no second authorization path: a tool can do only what its route lets the
// caller do.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
)

// maxResponseBytes bounds what a tool reads from one API response.
const maxResponseBytes = 8 << 20

// A Caller makes one REST API request as the MCP client's identity.
type Caller interface {
	// Do sends method path (path includes /api and any query) with body
	// as JSON when not nil, and returns the status and the response body.
	Do(ctx context.Context, method, path string, body any) (int, []byte, error)
}

// APIError is a REST API answer that is not a success, as a tool error the
// agent can act on.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "not signed in to Brokoli: " + e.Message
	case http.StatusForbidden:
		return "not permitted: " + e.Message
	case http.StatusNotFound:
		return "not found: " + e.Message
	}
	return fmt.Sprintf("Brokoli answered %d: %s", e.Status, e.Message)
}

// get calls GET path and decodes the JSON answer into out.
func get(ctx context.Context, c Caller, path string, out any) error {
	return call(ctx, c, http.MethodGet, path, nil, out)
}

func call(ctx context.Context, c Caller, method, path string, body, out any) error {
	status, data, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return &APIError{Status: status, Message: apiMessage(data)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("Brokoli's answer to %s %s is not what was expected: %w", method, path, err)
	}
	return nil
}

// apiMessage is the "error" of an API error body, or the body itself.
func apiMessage(data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return e.Error
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	if text == "" {
		return "no details"
	}
	return text
}

func encode(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

// forwardedHeaders are the request headers that carry who the caller is and
// which workspace they act in. Nothing else of the MCP request is passed on.
var forwardedHeaders = []string{"Authorization", "X-API-Key", "Cookie", "X-Workspace-ID"}

// RouterCaller sends requests through handler (the API server's router),
// in process, as the identity of the MCP request it was made for.
type RouterCaller struct {
	Handler http.Handler
	header  http.Header
	remote  string
}

// NewRouterCaller is a Caller for handler acting as the sender of r.
func NewRouterCaller(handler http.Handler, r *http.Request) *RouterCaller {
	h := http.Header{}
	for _, k := range forwardedHeaders {
		for _, v := range r.Header.Values(k) {
			h.Add(k, v)
		}
	}
	return &RouterCaller{Handler: handler, header: h, remote: r.RemoteAddr}
}

func (c *RouterCaller) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/api/mcp") {
		return 0, nil, fmt.Errorf("not an API path: %s", path)
	}
	reader, err := encode(body)
	if err != nil {
		return 0, nil, err
	}
	// A fresh context, cancelled with ctx but carrying none of its values:
	// the sub-request must be authenticated from the forwarded headers
	// alone, not from the identity the outer request's middleware put in
	// its context, and a router reached again from inside one of its own
	// handlers would otherwise resume the outer request's routing state
	// (chi does) and route the wrong path.
	fresh, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	req := httptest.NewRequestWithContext(fresh, method, path, reader)
	req.Header = c.header.Clone()
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = c.remote
	rec := httptest.NewRecorder()
	c.Handler.ServeHTTP(rec, req)
	data := rec.Body.Bytes()
	if len(data) > maxResponseBytes {
		data = data[:maxResponseBytes]
	}
	return rec.Code, data, nil
}

// HTTPCaller talks to a Brokoli server over HTTP, for `brokoli mcp`.
type HTTPCaller struct {
	Server    string // e.g. https://brokoli.example.com
	Token     string // sent as a bearer token when set
	Workspace string // sent as X-Workspace-ID when set
	Client    *http.Client
}

func (c *HTTPCaller) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	reader, err := encode(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Server, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Workspace != "" {
		req.Header.Set("X-Workspace-ID", c.Workspace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.Client
	if client == nil {
		// The person's own Brokoli server, often localhost, which the
		// server-side outbound policy would refuse; this runs only in
		// `brokoli mcp`, on their machine, never in the server.
		//netguard:allow client-side CLI call to the user's configured Brokoli server
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach Brokoli at %s: %w", c.Server, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}
