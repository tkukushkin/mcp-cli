package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerRoundTripper struct {
	headers map[string]string
	host    string
}

// RoundTrip adds the configured headers, but only on the host the config named. net/http
// strips Authorization when a redirect leaves the origin, and this runs below that: re-adding
// the header here would hand the token to wherever the server pointed.
func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Host, t.host) {
		return http.DefaultTransport.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	for name, value := range t.headers {
		// The transport sets the protocol's own headers — Accept, Content-Type, the session
		// id — and overwriting those with a configured one breaks the handshake.
		if req.Header.Get(name) == "" {
			req.Header.Set(name, value)
		}
	}
	return http.DefaultTransport.RoundTrip(req)
}

// authorize adds the OAuth token Claude Code holds for this server, unless the config
// already carries an Authorization header of its own.
func authorize(ctx context.Context, cfg *serverConfig) (map[string]string, error) {
	headers := maps.Clone(cfg.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	// Header names in the config keep the case they were written in, while HTTP ignores it.
	for name := range headers {
		if strings.EqualFold(name, "Authorization") {
			return headers, nil
		}
	}
	credentials, err := readClaudeCredentials(ctx)
	if err != nil {
		return nil, err
	}
	token, err := oauthToken(ctx, credentials, cfg.Name, cfg.URL)
	if err != nil {
		return nil, err
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers, nil
}

func newTransport(ctx context.Context, cfg *serverConfig, errlog io.Writer) (mcp.Transport, error) {
	if cfg.URL != "" {
		endpoint, err := url.Parse(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("server %q has an unusable url: %w", cfg.Name, err)
		}
		headers, err := authorize(ctx, cfg)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Transport: &headerRoundTripper{headers: headers, host: endpoint.Host}}
		if cfg.Type == "sse" {
			return &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: client}, nil
		}
		return &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client}, nil
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("server config has neither %q nor %q", "url", "command")
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for name, value := range cfg.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Stderr = errlog
	return &mcp.CommandTransport{Command: cmd}, nil
}

func callTool(ctx context.Context, cfg *serverConfig, tool string, arguments map[string]any, errlog io.Writer) (*mcp.CallToolResult, error) {
	transport, err := newTransport(ctx, cfg, errlog)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-cli", Version: getVersion()}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
}

// renderPayload strips the MCP envelope: structured content wins, then text blocks, then the raw result.
func renderPayload(result *mcp.CallToolResult) (string, error) {
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		return string(encoded), err
	}
	var texts []string
	for _, block := range result.Content {
		if text, ok := block.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n"), nil
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
