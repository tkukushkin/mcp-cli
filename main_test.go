package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	go func() {
		defer writer.Close()
		writer.WriteString(content)
	}()
	return reader
}

func TestReadArgumentsParsesJSONObject(t *testing.T) {
	arguments, err := readArguments(pipeWith(t, `{"a": 1, "b": "two"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 2 || arguments["b"] != "two" {
		t.Errorf("got %v", arguments)
	}
}

func TestReadArgumentsTreatsEmptyStdinAsNoArguments(t *testing.T) {
	arguments, err := readArguments(pipeWith(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 0 {
		t.Errorf("got %v", arguments)
	}
}

func TestReadArgumentsTreatsBlankStdinAsNoArguments(t *testing.T) {
	arguments, err := readArguments(pipeWith(t, "  \n\t "))
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 0 {
		t.Errorf("got %v", arguments)
	}
}

// A character device — /dev/null, or a terminal — means the caller passed no arguments.
func TestReadArgumentsTreatsCharacterDeviceAsNoArguments(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()

	arguments, err := readArguments(devNull)
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 0 {
		t.Errorf("got %v", arguments)
	}
}

func TestReadArgumentsRejectsInvalidJSON(t *testing.T) {
	if _, err := readArguments(pipeWith(t, "not json")); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestReadArgumentsRejectsNonObjectJSON(t *testing.T) {
	if _, err := readArguments(pipeWith(t, `[1, 2, 3]`)); err == nil {
		t.Fatal("expected an error for a JSON array")
	}
}

func TestReadArgumentsRejectsClosedStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	reader.Close()

	if _, err := readArguments(reader); err == nil {
		t.Fatal("expected an error for a closed file")
	}
}

func TestGetVersionPrefersTheStampedValue(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "v1.2.3"

	if got := getVersion(); got != "v1.2.3" {
		t.Errorf("got %q, want %q", got, "v1.2.3")
	}
}

func TestGetVersionFallsBackWithoutAStamp(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "dev"

	if got := getVersion(); got == "" {
		t.Error("expected a non-empty fallback version")
	}
}

// runCommand executes the CLI against a project-scoped config holding the given servers.
func runCommand(t *testing.T, servers map[string]*serverConfig, stdin string, args ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	config, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".mcp.json"), string(config))
	t.Chdir(dir)
	t.Setenv("HOME", dir)

	originalStdin := os.Stdin
	os.Stdin = pipeWith(t, stdin)
	t.Cleanup(func() { os.Stdin = originalStdin })

	var out bytes.Buffer
	cmd := callToolCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err = cmd.Execute()
	return out.String(), err
}

func TestCommandPrintsToolPayload(t *testing.T) {
	out, err := runCommand(t, map[string]*serverConfig{"mock": stdioConfig(t)},
		`{"message": "hello"}`, "mock", "echo")
	if err != nil {
		t.Fatal(err)
	}
	if out != "{\"echoed\":\"hello\"}\n" {
		t.Errorf("got %q", out)
	}
}

func TestCommandFailsOnToolError(t *testing.T) {
	_, err := runCommand(t, map[string]*serverConfig{"mock": stdioConfig(t)}, "", "mock", "boom")
	if err == nil || !strings.Contains(err.Error(), "tool exploded") {
		t.Fatalf("got %v, want the tool's own error", err)
	}
}

func TestCommandFailsOnUnknownServer(t *testing.T) {
	_, err := runCommand(t, map[string]*serverConfig{"mock": stdioConfig(t)}, "", "other", "echo")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v, want a server lookup error", err)
	}
}

func TestCommandFailsOnInvalidStdin(t *testing.T) {
	_, err := runCommand(t, map[string]*serverConfig{"mock": stdioConfig(t)}, "not json", "mock", "echo")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("got %v, want a stdin parse error", err)
	}
}

func TestCommandRejectsWrongArgumentCount(t *testing.T) {
	if _, err := runCommand(t, nil, "", "mock"); err == nil {
		t.Fatal("expected an error for a missing tool argument")
	}
}

func TestCommandReportsVersion(t *testing.T) {
	out, err := runCommand(t, nil, "", "--version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != getVersion() {
		t.Errorf("got %q, want %q", out, getVersion())
	}
}

func TestCommandVerboseForwardsServerStderr(t *testing.T) {
	out, err := runCommand(t, map[string]*serverConfig{"mock": stdioConfig(t)},
		`{"message": "hello"}`, "-v", "mock", "echo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `{"echoed":"hello"}`) {
		t.Errorf("got %q", out)
	}
}
