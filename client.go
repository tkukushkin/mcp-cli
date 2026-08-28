package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerRoundTripper struct {
	headers map[string]string
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for name, value := range t.headers {
		req.Header.Set(name, value)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func newTransport(cfg *serverConfig, errlog io.Writer) (mcp.Transport, error) {
	if cfg.URL != "" {
		return &mcp.StreamableClientTransport{
			Endpoint:   cfg.URL,
			HTTPClient: &http.Client{Transport: &headerRoundTripper{headers: cfg.Headers}},
		}, nil
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("server config has neither %q nor %q", "url", "command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = os.Environ()
	for name, value := range cfg.Env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Stderr = errlog
	return &mcp.CommandTransport{Command: cmd}, nil
}

func callTool(ctx context.Context, cfg *serverConfig, tool string, arguments map[string]any, errlog io.Writer) (*mcp.CallToolResult, error) {
	transport, err := newTransport(cfg, errlog)
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
