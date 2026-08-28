package main

import (
	"bytes"
	"io"
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

	transport, err := newTransport(&serverConfig{Command: "srv", Args: []string{"-x"}, Env: map[string]string{"TOKEN": "secret"}}, os.Stderr)
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

func TestNewTransportHTTP(t *testing.T) {
	transport, err := newTransport(&serverConfig{URL: "https://example.test/mcp"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := transport.(*mcp.StreamableClientTransport); !ok {
		t.Fatalf("got %T, want *mcp.StreamableClientTransport", transport)
	}
}

func TestNewTransportRejectsEmptyConfig(t *testing.T) {
	if _, err := newTransport(&serverConfig{}, os.Stderr); err == nil {
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
