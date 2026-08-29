package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverEnv makes the test binary serve MCP over stdio instead of running tests, so the
// stdio transport is exercised against a real server without building a second binary.
const serverEnv = "MCP_CLI_TEST_SERVER"

const tokenEnv = "MCP_CLI_TEST_TOKEN"

func TestMain(m *testing.M) {
	if os.Getenv(serverEnv) == "" {
		// Keep every test off the real Keychain, so none of them can send a real token
		// to a test server. Credentials then come from CLAUDE_CONFIG_DIR alone.
		keychainCredentials = func() ([]byte, error) {
			return nil, errors.New("keychain disabled in tests")
		}
		writeKeychainCredentials = func([]byte) error {
			return errors.New("keychain disabled in tests")
		}
		os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(os.TempDir(), "mcp-cli-without-credentials"))
		os.Exit(m.Run())
	}
	fmt.Fprintln(os.Stderr, "mock server started")
	if err := newTestServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type echoInput struct {
	Message string `json:"message"`
}

type echoOutput struct {
	Echoed string `json:"echoed"`
}

func newTestServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mock", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{Echoed: in.Message}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "token"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv(tokenEnv)}}}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "boom"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("tool exploded")
		})
	return server
}

// stdioConfig points at the test binary itself, by absolute path so that it survives t.Chdir.
func stdioConfig(t *testing.T) *serverConfig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &serverConfig{Command: self, Env: map[string]string{serverEnv: "1"}}
}

// headerRecorder captures the headers of the last request the HTTP test server received.
type headerRecorder struct {
	mu   sync.Mutex
	last http.Header
}

func (r *headerRecorder) record(h http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = h.Clone()
}

func (r *headerRecorder) get(name string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last.Get(name)
}

func httpConfig(t *testing.T, headers map[string]string) (*serverConfig, *headerRecorder) {
	t.Helper()
	recorder := &headerRecorder{}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return newTestServer() }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header)
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return &serverConfig{URL: srv.URL, Headers: headers}, recorder
}
