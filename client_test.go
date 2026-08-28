package main

import (
	"os"
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
