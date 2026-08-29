package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRenderPayloadPrefersStructuredContent(t *testing.T) {
	result := &mcp.CallToolResult{
		StructuredContent: map[string]any{"a": 1},
		Content:           []mcp.Content{&mcp.TextContent{Text: "ignored"}},
	}

	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if payload != `{"a":1}` {
		t.Errorf("got %q", payload)
	}
}

func TestRenderPayloadJoinsTextBlocks(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.TextContent{Text: "one"},
		&mcp.ImageContent{Data: []byte("png")},
		&mcp.TextContent{Text: "two"},
	}}

	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if payload != "one\ntwo" {
		t.Errorf("got %q", payload)
	}
}

func TestRenderPayloadFallsBackToWholeResult(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("png")}}}

	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"type":"image"`) {
		t.Errorf("got %q", payload)
	}
}

func TestNewTransportStdioMergesEnv(t *testing.T) {
	t.Setenv("MCP_CLI_TEST_INHERITED", "yes")

	transport, err := newTransport(t.Context(), &serverConfig{Command: "srv", Args: []string{"-x"}, Env: map[string]string{"TOKEN": "secret"}}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	command, ok := transport.(*mcp.CommandTransport)
	if !ok {
		t.Fatalf("got %T, want *mcp.CommandTransport", transport)
	}
	env := strings.Join(command.Command.Env, "\n")
	if !strings.Contains(env, "MCP_CLI_TEST_INHERITED=yes") || !strings.Contains(env, "TOKEN=secret") {
		t.Errorf("env not merged: %v", command.Command.Args)
	}
}

func TestNewTransportStdioSetsCwd(t *testing.T) {
	dir := t.TempDir()

	transport, err := newTransport(t.Context(), &serverConfig{Command: "pwd", Cwd: dir}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	command, ok := transport.(*mcp.CommandTransport)
	if !ok {
		t.Fatalf("got %T, want *mcp.CommandTransport", transport)
	}
	if command.Command.Dir != dir {
		t.Errorf("cmd.Dir = %q, want %q", command.Command.Dir, dir)
	}
}

func TestNewTransportHTTP(t *testing.T) {
	transport, err := newTransport(t.Context(), &serverConfig{URL: "https://example.test/mcp"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("got %T, want *mcp.StreamableClientTransport", transport)
	}
}

func TestNewTransportSSE(t *testing.T) {
	transport, err := newTransport(t.Context(), &serverConfig{Type: "sse", URL: "https://example.test/sse"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transport.(*mcp.SSEClientTransport); !ok {
		t.Fatalf("got %T, want *mcp.SSEClientTransport", transport)
	}
}

// net/http drops Authorization when a redirect leaves the origin. Re-adding the configured
// headers below that would hand the token to whatever host the server pointed at.
func TestHeaderRoundTripperDropsHeadersOffTheConfiguredHost(t *testing.T) {
	elsewhere := &headerRecorder{}
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		elsewhere.record(r.Header)
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/mcp", http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	endpoint, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: &headerRoundTripper{
		headers: map[string]string{"Authorization": "Bearer secret"},
		host:    endpoint.Host,
	}}
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	if got := elsewhere.get("Authorization"); got != "" {
		t.Errorf("the token was sent to the redirect target: %q", got)
	}
}

// The transport sets the protocol's own headers; a configured one must not overwrite them.
func TestHeaderRoundTripperKeepsTheProtocolHeaders(t *testing.T) {
	recorder := &headerRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
	}))
	t.Cleanup(server.Close)
	request, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")

	transport := &headerRoundTripper{
		headers: map[string]string{"Accept": "application/json", "X-Trace": "abc"},
		host:    request.URL.Host,
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	if got := recorder.get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want the transport's own value", got)
	}
	if got := recorder.get("X-Trace"); got != "abc" {
		t.Errorf("X-Trace = %q, want the configured value", got)
	}
}

func TestNewTransportRejectsEmptyConfig(t *testing.T) {
	if _, err := newTransport(t.Context(), &serverConfig{}, os.Stderr); err == nil {
		t.Fatal("expected an error for a config with neither url nor command")
	}
}

func TestCallToolOverStdio(t *testing.T) {
	t.Setenv(tokenEnv, "from-parent-env")

	result, err := callTool(t.Context(), stdioConfig(t), "echo", map[string]any{"message": "hi"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if payload != `{"echoed":"hi"}` {
		t.Errorf("got %q", payload)
	}
}

func TestCallToolOverStdioMergesConfiguredEnv(t *testing.T) {
	cfg := stdioConfig(t)
	cfg.Env[tokenEnv] = "from-config"

	result, err := callTool(t.Context(), cfg, "token", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if payload != "from-config" {
		t.Errorf("got %q, want the value from the server config", payload)
	}
}

func TestCallToolPassesServerStderrToErrlog(t *testing.T) {
	var errlog bytes.Buffer

	if _, err := callTool(t.Context(), stdioConfig(t), "echo", map[string]any{"message": "hi"}, &errlog); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errlog.String(), "mock server started") {
		t.Errorf("server stderr not forwarded, got %q", errlog.String())
	}
}

func TestCallToolReportsToolError(t *testing.T) {
	result, err := callTool(t.Context(), stdioConfig(t), "boom", nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected IsError")
	}
	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "tool exploded") {
		t.Errorf("got %q", payload)
	}
}

func TestCallToolUnknownTool(t *testing.T) {
	if _, err := callTool(t.Context(), stdioConfig(t), "nosuchtool", nil, io.Discard); err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
}

func TestCallToolCommandNotFound(t *testing.T) {
	cfg := &serverConfig{Command: filepath.Join(t.TempDir(), "nonexistent-server")}

	if _, err := callTool(t.Context(), cfg, "echo", nil, io.Discard); err == nil {
		t.Fatal("expected an error for a command that cannot be started")
	}
}

func TestCallToolOverHTTPSendsConfiguredHeaders(t *testing.T) {
	cfg, recorder := httpConfig(t, map[string]string{"Authorization": "Bearer secret"})

	result, err := callTool(t.Context(), cfg, "echo", map[string]any{"message": "over http"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := renderPayload(result)
	if err != nil {
		t.Fatal(err)
	}
	if payload != `{"echoed":"over http"}` {
		t.Errorf("got %q", payload)
	}
	if got := recorder.get("Authorization"); got != "Bearer secret" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer secret")
	}
}

func TestCallToolOverHTTPWithoutHeaders(t *testing.T) {
	cfg, _ := httpConfig(t, nil)

	if _, err := callTool(t.Context(), cfg, "echo", map[string]any{"message": "x"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestCallToolHTTPConnectionRefused(t *testing.T) {
	cfg, _ := httpConfig(t, nil)
	cfg.URL = "http://127.0.0.1:1/mcp"

	if _, err := callTool(t.Context(), cfg, "echo", nil, io.Discard); err == nil {
		t.Fatal("expected an error for an unreachable server")
	}
}

func TestCallToolRejectsEmptyConfig(t *testing.T) {
	if _, err := callTool(t.Context(), &serverConfig{}, "echo", nil, io.Discard); err == nil {
		t.Fatal("expected an error for a config with neither url nor command")
	}
}
