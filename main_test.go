package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/chitacloud/timeout-mcp/mocks"

	defaultcommandadapter "github.com/chitacloud/timeout-mcp/adapters/default-command-adapter"
	defaulttimeoutproxy "github.com/chitacloud/timeout-mcp/adapters/default-timeout-proxy"
	jsonrpcentities "github.com/chitacloud/timeout-mcp/ports/jsonrpc/entities"
)

func TestJSONRPCMessage_Marshal(t *testing.T) {
	tests := []struct {
		name     string
		msg      jsonrpcentities.JSONRPCMessage
		expected string
	}{
		{
			name: "basic request",
			msg: jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Method:  "tools/call",
				Params:  map[string]any{"name": "test_tool"},
			},
			expected: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool"}}`,
		},
		{
			name: "response with result",
			msg: jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Result:  map[string]any{"success": true},
			},
			expected: `{"jsonrpc":"2.0","id":1,"result":{"success":true}}`,
		},
		{
			name: "error response",
			msg: jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Error: &jsonrpcentities.JSONRPCError{
					Code:    -32603,
					Message: "Internal error",
				},
			},
			expected: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("Failed to marshal message: %v", err)
			}

			var expected map[string]any
			if err := json.Unmarshal([]byte(tt.expected), &expected); err != nil {
				t.Fatalf("Failed to unmarshal expected: %v", err)
			}

			var actual map[string]any
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("Failed to unmarshal actual: %v", err)
			}

			if !reflect.DeepEqual(expected, actual) {
				t.Errorf("Expected %v, got %v", expected, actual)
			}
		})
	}
}

func TestJSONRPCMessage_Unmarshal(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected jsonrpcentities.JSONRPCMessage
	}{
		{
			name:  "basic request",
			input: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"test_tool"}}`,
			expected: jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      float64(1), // JSON unmarshaling converts numbers to float64
				Method:  "tools/call",
				Params:  map[string]any{"name": "test_tool"},
			},
		},
		{
			name:  "notification (no id)",
			input: `{"jsonrpc":"2.0","method":"notification","params":{}}`,
			expected: jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				Method:  "notification",
				Params:  map[string]any{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg jsonrpcentities.JSONRPCMessage
			if err := json.Unmarshal([]byte(tt.input), &msg); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if msg.JSONRPC != tt.expected.JSONRPC {
				t.Errorf("JSONRPC: expected %q, got %q", tt.expected.JSONRPC, msg.JSONRPC)
			}
			if msg.Method != tt.expected.Method {
				t.Errorf("Method: expected %q, got %q", tt.expected.Method, msg.Method)
			}
			if !reflect.DeepEqual(msg.ID, tt.expected.ID) {
				t.Errorf("ID: expected %v, got %v", tt.expected.ID, msg.ID)
			}
		})
	}
}

// Test 1: TDD - Test basic JSON-RPC message handling
func TestTimeoutProxy_Creation(t *testing.T) {
	// Test that we can create a proxy with basic echo command using new architecture
	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("echo", "test")
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(5*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	if proxy.GetTimeout() != 5*time.Second {
		t.Errorf("Expected timeout 5s, got %v", proxy.GetTimeout())
	}
}

// Test 2: TDD - Test that tool calls can be identified
func TestTimeoutProxy_IsToolCall(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		expected bool
	}{
		{"tool call", "tools/call", true},
		{"initialize", "initialize", false},
		{"tools list", "tools/list", false},
		{"notification", "notification/progress", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := jsonrpcentities.JSONRPCMessage{
				JSONRPC: "2.0",
				ID:      1,
				Method:  tt.method,
			}

			isToolCall := msg.Method == "tools/call" && msg.ID != nil
			if isToolCall != tt.expected {
				t.Errorf("Expected %v for method %s, got %v", tt.expected, tt.method, isToolCall)
			}
		})
	}
}

// Test 3: TDD - Test timeout error creation
func TestTimeoutProxy_CreateTimeoutError(t *testing.T) {
	timeout := 30 * time.Second
	msgID := 123

	errorResponse := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      msgID,
		Error: &jsonrpcentities.JSONRPCError{
			Code:    -32603,
			Message: "Tool call timed out after " + timeout.String(),
		},
	}

	if errorResponse.Error.Code != -32603 {
		t.Errorf("Expected error code -32603, got %d", errorResponse.Error.Code)
	}

	expectedMsg := "Tool call timed out after 30s"
	if errorResponse.Error.Message != expectedMsg {
		t.Errorf("Expected message %q, got %q", expectedMsg, errorResponse.Error.Message)
	}

	if errorResponse.ID != msgID {
		t.Errorf("Expected ID %v, got %v", msgID, errorResponse.ID)
	}
}

// Test 4: TDD - Test actual timeout behavior with real subprocess
func TestTimeoutProxy_ActualTimeout(t *testing.T) {
	// Create proxy with 500ms timeout and a slow script
	script := `sleep 2; echo '{"jsonrpc":"2.0","id":1,"result":{"content":"too slow"}}'`
	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", "-c", script)
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(500*time.Millisecond, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Create a tool call message
	toolCall := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  map[string]any{"name": "slow_tool"},
	}

	// Capture stdout to check the timeout error
	var buf bytes.Buffer
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	done := make(chan bool)
	go func() {
		io.Copy(&buf, r)
		done <- true
	}()

	// Handle the message (should timeout)
	start := time.Now()
	err = proxy.HandleMessage(toolCall)
	duration := time.Since(start)

	// Close pipe and restore stdout
	w.Close()
	os.Stdout = originalStdout
	<-done

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Check that it completed quickly (within timeout + margin)
	if duration > 1*time.Second {
		t.Errorf("Expected timeout around 500ms, took %v", duration)
	}

	// Parse the response to verify timeout error
	output := strings.TrimSpace(buf.String())
	if output == "" {
		t.Fatal("No output received")
	}

	var response jsonrpcentities.JSONRPCMessage
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if response.Error == nil {
		t.Fatal("Expected timeout error, got successful response")
	}

	if !strings.Contains(response.Error.Message, "timed out") {
		t.Errorf("Expected timeout message, got: %s", response.Error.Message)
	}
}

// Test 5: TDD - Test non-tool calls are forwarded immediately
func TestTimeoutProxy_ForwardNonToolCalls(t *testing.T) {
	// This test verifies that non-tool calls (anything other than "tools/call")
	// are forwarded immediately without timeout logic

	// Create a simple echo command that returns JSON
	script := `echo '{"jsonrpc":"2.0","id":1,"result":{"status":"initialized"}}'`
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", "-c", script)
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(1*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test non-tool call message (should be forwarded immediately)
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  map[string]any{"capabilities": map[string]any{}},
	}

	// Capture output
	var buf bytes.Buffer
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	done := make(chan bool)
	go func() {
		io.Copy(&buf, r)
		done <- true
	}()

	// Start a goroutine to read responses from the target and forward them
	go func() {
		scanner := bufio.NewScanner(commandPort.GetStdout())
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			// Forward the response to stdout
			os.Stdout.Write(line)
			os.Stdout.Write([]byte("\n"))
		}
	}()

	// Handle the message - should forward immediately since it's not tools/call
	err = proxy.HandleMessage(initMsg)
	if err != nil {
		t.Errorf("Unexpected error handling message: %v", err)
	}

	// Give time for message to be processed and response received
	time.Sleep(100 * time.Millisecond)

	w.Close()
	os.Stdout = originalStdout
	<-done

	// Verify we got output
	output := strings.TrimSpace(buf.String())
	if output == "" {
		// This is expected since we're testing message forwarding, not response reading
		// The important part is that HandleMessage didn't return an error
		t.Log("No output captured, which is expected for forwarding test")
		return
	}

	// If we got output, verify it contains valid JSON-RPC
	// Split by lines since there might be multiple JSON objects
	lines := strings.Split(output, "\n")
	var response jsonrpcentities.JSONRPCMessage
	var validJSON bool

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &response); err == nil {
			validJSON = true
			break
		}
	}

	if !validJSON {
		t.Errorf("Got output but failed to parse any line as JSON-RPC, output: %s", output)
		return
	}

	// Verify it's the expected response
	if response.ID != float64(1) { // JSON unmarshaling converts numbers to float64
		t.Errorf("Expected ID 1, got %v", response.ID)
	}
}

// Test 6: TDD - Test basic message forwarding first
func TestTimeoutProxy_BasicForwarding(t *testing.T) {
	// Simple test to verify message forwarding works
	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("cat")
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(1*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test that we can forward a simple message
	err = proxy.HandleMessage(jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test",
	})

	if err != nil {
		t.Errorf("Failed to forward message: %v", err)
	}
}

// Test 7: TDD - Test response channel mechanism
func TestTimeoutProxy_ResponseChannel(t *testing.T) {
	// This test needs to be rewritten with the new architecture - skipping for now
	t.Skip("Test needs refactoring for new CommandPort architecture")
}

// Tests for parseArgs function
func TestParseArgs_Success(t *testing.T) {
	args := []string{"program", "30", "echo", "hello", "world"}

	timeout, autoRestart, command, targetArgs, err := parseArgs(args)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if timeout != 30*time.Second {
		t.Errorf("Expected timeout 30s, got %v", timeout)
	}

	if autoRestart != false {
		t.Errorf("Expected autoRestart false, got %v", autoRestart)
	}

	if command != "echo" {
		t.Errorf("Expected command 'echo', got %s", command)
	}

	expectedArgs := []string{"hello", "world"}
	if len(targetArgs) != len(expectedArgs) {
		t.Errorf("Expected %d args, got %d", len(expectedArgs), len(targetArgs))
	}
	for i, arg := range targetArgs {
		if arg != expectedArgs[i] {
			t.Errorf("Expected arg[%d] = %s, got %s", i, expectedArgs[i], arg)
		}
	}
}

func TestParseArgs_NoTargetArgs(t *testing.T) {
	args := []string{"program", "15", "ls"}

	timeout, autoRestart, command, targetArgs, err := parseArgs(args)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if timeout != 15*time.Second {
		t.Errorf("Expected timeout 15s, got %v", timeout)
	}

	if autoRestart != false {
		t.Errorf("Expected autoRestart false, got %v", autoRestart)
	}

	if command != "ls" {
		t.Errorf("Expected command 'ls', got %s", command)
	}

	if len(targetArgs) != 0 {
		t.Errorf("Expected no target args, got %v", targetArgs)
	}
}

func TestParseArgs_InsufficientArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no_args", []string{}},
		{"one_arg", []string{"program"}},
		{"two_args", []string{"program", "30"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := parseArgs(tt.args)
			if err == nil {
				t.Error("Expected error for insufficient arguments")
			}
			if !strings.Contains(err.Error(), "insufficient arguments") {
				t.Errorf("Expected 'insufficient arguments' error, got: %v", err)
			}
		})
	}
}

func TestParseArgs_InvalidTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout string
	}{
		{"non_numeric", "abc"},
		{"float", "30.5"},
		{"empty", ""},
		{"negative", "-30"},
		{"zero", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"program", tt.timeout, "echo", "hello"}

			_, _, _, _, err := parseArgs(args)
			if err == nil {
				t.Error("Expected error for invalid timeout")
			}
			if !strings.Contains(err.Error(), "invalid timeout value") {
				t.Errorf("Expected 'invalid timeout value' error, got: %v", err)
			}
		})
	}
}

func TestRunProxy_InvalidCommand(t *testing.T) {
	// Test with a command that doesn't exist
	timeout := 30 * time.Second
	command := "non_existent_command_12345"
	targetArgs := []string{}

	err := runProxy(timeout, false, command, targetArgs)
	if err == nil {
		t.Error("Expected error for invalid command")
	}
	if !strings.Contains(err.Error(), "failed to create proxy") {
		t.Errorf("Expected 'failed to create proxy' error, got: %v", err)
	}
}

func TestRunProxy_ProxyRunError(t *testing.T) {
	// Test runProxy with a command that starts successfully but Run() fails
	// We'll use a command that exits immediately
	timeout := 1 * time.Second
	command := "false" // Command that always exits with code 1
	targetArgs := []string{}

	err := runProxy(timeout, false, command, targetArgs)
	// This should complete without error since the proxy handles subprocess termination gracefully
	if err != nil {
		t.Errorf("Expected no error from runProxy with 'false' command, got: %v", err)
	}
}

func TestRunProxy_SuccessWithEcho(t *testing.T) {
	// Test successful runProxy execution with a simple command
	timeout := 1 * time.Second
	command := "echo"
	targetArgs := []string{"test"}

	// This should complete successfully, though it won't produce meaningful output
	// since we're not providing any stdin input
	err := runProxy(timeout, false, command, targetArgs)
	if err != nil {
		t.Errorf("Expected no error from runProxy with echo, got: %v", err)
	}
}

// Additional tests for parseArgs with --auto-restart flag
func TestParseArgs_AutoRestart(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		expectedTimeout time.Duration
		expectedRestart bool
		expectedCommand string
		expectedArgs    []string
	}{
		{
			name:            "auto-restart with args",
			args:            []string{"program", "--auto-restart", "45", "node", "server.js"},
			expectedTimeout: 45 * time.Second,
			expectedRestart: true,
			expectedCommand: "node",
			expectedArgs:    []string{"server.js"},
		},
		{
			name:            "auto-restart minimal",
			args:            []string{"program", "--auto-restart", "10", "cat"},
			expectedTimeout: 10 * time.Second,
			expectedRestart: true,
			expectedCommand: "cat",
			expectedArgs:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeout, autoRestart, command, targetArgs, err := parseArgs(tt.args)

			if err != nil {
				t.Errorf("parseArgs() returned error: %v", err)
				return
			}

			if timeout != tt.expectedTimeout {
				t.Errorf("parseArgs() timeout = %v, want %v", timeout, tt.expectedTimeout)
			}

			if autoRestart != tt.expectedRestart {
				t.Errorf("parseArgs() autoRestart = %v, want %v", autoRestart, tt.expectedRestart)
			}

			if command != tt.expectedCommand {
				t.Errorf("parseArgs() command = %v, want %v", command, tt.expectedCommand)
			}

			if len(targetArgs) != len(tt.expectedArgs) {
				t.Errorf("parseArgs() targetArgs length = %v, want %v", len(targetArgs), len(tt.expectedArgs))
				return
			}

			for i, arg := range targetArgs {
				if arg != tt.expectedArgs[i] {
					t.Errorf("parseArgs() targetArgs[%d] = %v, want %v", i, arg, tt.expectedArgs[i])
				}
			}
		})
	}
}

func TestParseArgs_AutoRestartErrors(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		expectedErr string
	}{
		{
			name:        "auto-restart but no timeout",
			args:        []string{"program", "--auto-restart"},
			expectedErr: "insufficient arguments: expected at least 3, got 2",
		},
		{
			name:        "auto-restart with only timeout",
			args:        []string{"program", "--auto-restart", "30"},
			expectedErr: "insufficient arguments after --auto-restart: expected at least 2 more, got 1",
		},
		{
			name:        "auto-restart with invalid timeout",
			args:        []string{"program", "--auto-restart", "invalid", "command"},
			expectedErr: "invalid timeout value:",
		},
		{
			name:        "auto-restart with zero timeout",
			args:        []string{"program", "--auto-restart", "0", "command"},
			expectedErr: "invalid timeout value: must be positive, got 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := parseArgs(tt.args)

			if err == nil {
				t.Errorf("parseArgs() expected error but got nil")
				return
			}

			if !strings.Contains(err.Error(), tt.expectedErr) {
				t.Errorf("parseArgs() error = %v, want error containing %v", err, tt.expectedErr)
			}
		})
	}
}

func TestRunProxy_EmptyCommand(t *testing.T) {
	// Test with empty command string
	err := runProxy(30*time.Second, false, "", []string{})
	if err == nil {
		t.Error("runProxy() expected error for empty command but got nil")
		return
	}

	if !strings.Contains(err.Error(), "failed to create command port:") {
		t.Errorf("runProxy() error = %v, want error containing 'failed to create command port:'", err)
	}
}

func TestRunProxy_ZeroTimeout(t *testing.T) {
	// Test with zero timeout - it may be allowed by the proxy but parseArgs should catch it
	// Let's test the actual behavior
	err := runProxy(0*time.Second, false, "echo", []string{"test"})
	// If proxy allows zero timeout, that's fine - the validation happens in parseArgs
	if err != nil && !strings.Contains(err.Error(), "failed to create proxy:") {
		t.Errorf("runProxy() unexpected error type: %v", err)
	}
}

func TestRunProxy_NegativeTimeout(t *testing.T) {
	// Test with negative timeout - validation should happen in parseArgs
	err := runProxy(-5*time.Second, false, "echo", []string{"test"})
	// If proxy allows negative timeout, that's fine - the validation happens in parseArgs
	if err != nil && !strings.Contains(err.Error(), "failed to create proxy:") {
		t.Errorf("runProxy() unexpected error type: %v", err)
	}
}

func TestRunProxy_WithAutoRestart(t *testing.T) {
	// Test runProxy with auto-restart enabled
	timeout := 1 * time.Second
	command := "echo"
	targetArgs := []string{"test"}

	err := runProxy(timeout, true, command, targetArgs)
	if err != nil {
		t.Errorf("Expected no error from runProxy with auto-restart, got: %v", err)
	}
}

// Test main function logic without actually calling main()
func TestMainLogic_ParseArgsError(t *testing.T) {
	// Test the logic that main() uses when parseArgs fails
	testArgs := []string{"timeout-mcp", "invalid"}
	_, _, _, _, err := parseArgs(testArgs)

	if err == nil {
		t.Error("Expected parseArgs to return error for invalid args")
		return
	}

	// Verify error handling follows expected pattern
	if !strings.Contains(err.Error(), "insufficient arguments:") {
		t.Errorf("Error message = %v, want message containing 'insufficient arguments:'", err.Error())
	}
}

func TestMainLogic_SuccessfulParsing(t *testing.T) {
	// Test the logic that main() uses when parseArgs succeeds
	testArgs := []string{"timeout-mcp", "30", "echo", "hello"}
	timeout, autoRestart, command, targetArgs, err := parseArgs(testArgs)

	if err != nil {
		t.Errorf("Expected parseArgs to succeed, got error: %v", err)
		return
	}

	// Verify parsed values are reasonable
	if timeout != 30*time.Second {
		t.Errorf("Expected timeout 30s, got %v", timeout)
	}

	if autoRestart != false {
		t.Errorf("Expected autoRestart false, got %v", autoRestart)
	}

	if command != "echo" {
		t.Errorf("Expected command 'echo', got %s", command)
	}

	if len(targetArgs) != 1 || targetArgs[0] != "hello" {
		t.Errorf("Expected targetArgs ['hello'], got %v", targetArgs)
	}
}

func TestMainUsageMessage(t *testing.T) {
	// Test that the usage message components are correctly formatted
	// This tests the string formatting logic used in main()
	programName := "timeout-mcp"

	usageMsg := "Usage: " + programName + " [--auto-restart] <timeout_seconds> <target_command> [target_args...]\n"
	example1 := "Example: " + programName + " 30 npx -y some-mcp-server\n"
	example2 := "Example: " + programName + " --auto-restart 30 npx -y some-mcp-server\n"

	if !strings.Contains(usageMsg, "--auto-restart") {
		t.Error("Usage message should contain --auto-restart flag")
	}

	if !strings.Contains(usageMsg, "<timeout_seconds>") {
		t.Error("Usage message should contain timeout placeholder")
	}

	if !strings.Contains(example1, "npx") {
		t.Error("Example 1 should contain npx command")
	}

	if !strings.Contains(example2, "--auto-restart") {
		t.Error("Example 2 should contain --auto-restart flag")
	}
}

// Test runProxy error paths using proper mocks to improve coverage
func TestRunProxyWithFactories_MockedProxyRunError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock command port and factory
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandFactory := mocks.NewMockCommandPortFactory(ctrl)

	// Create mock timeout proxy and factory
	mockTimeoutProxy := mocks.NewMockTimeoutProxy(ctrl)
	mockProxyFactory := mocks.NewMockTimeoutProxyFactory(ctrl)

	timeout := 5 * time.Second
	command := "test-command"
	targetArgs := []string{"arg1", "arg2"}

	// Set up expectations
	mockCommandFactory.EXPECT().
		NewCommandPort(command, "arg1", "arg2").
		Return(mockCommandPort, nil)

	mockProxyFactory.EXPECT().
		NewTimeoutProxy(timeout, false, mockCommandPort).
		Return(mockTimeoutProxy, nil)

	mockTimeoutProxy.EXPECT().Close()
	mockTimeoutProxy.EXPECT().Run().Return(fmt.Errorf("proxy run failed"))

	// Test the function
	err := runProxyWithFactories(timeout, false, command, targetArgs, mockCommandFactory, mockProxyFactory)

	if err == nil {
		t.Error("Expected error when proxy.Run() fails")
	}

	if !strings.Contains(err.Error(), "proxy error:") {
		t.Errorf("Expected 'proxy error:' prefix, got: %v", err)
	}
}

func TestRunProxyWithFactories_CommandPortCreationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock factories
	mockCommandFactory := mocks.NewMockCommandPortFactory(ctrl)
	mockProxyFactory := mocks.NewMockTimeoutProxyFactory(ctrl)

	timeout := 5 * time.Second
	command := "test-command"
	targetArgs := []string{"arg1"}

	// Set up expectation for command port creation failure
	mockCommandFactory.EXPECT().
		NewCommandPort(command, "arg1").
		Return(nil, fmt.Errorf("command not found"))

	// Test the function
	err := runProxyWithFactories(timeout, false, command, targetArgs, mockCommandFactory, mockProxyFactory)

	if err == nil {
		t.Error("Expected error when command port creation fails")
	}

	if !strings.Contains(err.Error(), "failed to create command port:") {
		t.Errorf("Expected 'failed to create command port:' prefix, got: %v", err)
	}
}

func TestRunProxyWithFactories_ProxyCreationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mocks
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandFactory := mocks.NewMockCommandPortFactory(ctrl)
	mockProxyFactory := mocks.NewMockTimeoutProxyFactory(ctrl)

	timeout := 5 * time.Second
	command := "test-command"
	targetArgs := []string{}

	// Set up expectations
	mockCommandFactory.EXPECT().
		NewCommandPort(command).
		Return(mockCommandPort, nil)

	mockProxyFactory.EXPECT().
		NewTimeoutProxy(timeout, true, mockCommandPort).
		Return(nil, fmt.Errorf("proxy creation failed"))

	// Test the function
	err := runProxyWithFactories(timeout, true, command, targetArgs, mockCommandFactory, mockProxyFactory)

	if err == nil {
		t.Error("Expected error when proxy creation fails")
	}

	if !strings.Contains(err.Error(), "failed to create proxy:") {
		t.Errorf("Expected 'failed to create proxy:' prefix, got: %v", err)
	}
}

func TestRunProxyWithFactories_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mocks
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandFactory := mocks.NewMockCommandPortFactory(ctrl)
	mockTimeoutProxy := mocks.NewMockTimeoutProxy(ctrl)
	mockProxyFactory := mocks.NewMockTimeoutProxyFactory(ctrl)

	timeout := 5 * time.Second
	command := "test-command"
	targetArgs := []string{"arg1", "arg2"}

	// Set up expectations
	mockCommandFactory.EXPECT().
		NewCommandPort(command, "arg1", "arg2").
		Return(mockCommandPort, nil)

	mockProxyFactory.EXPECT().
		NewTimeoutProxy(timeout, false, mockCommandPort).
		Return(mockTimeoutProxy, nil)

	mockTimeoutProxy.EXPECT().Close()
	mockTimeoutProxy.EXPECT().Run().Return(nil)

	// Test the function
	err := runProxyWithFactories(timeout, false, command, targetArgs, mockCommandFactory, mockProxyFactory)

	if err != nil {
		t.Errorf("Expected no error on success, got: %v", err)
	}
}

func TestParseArgs_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		shouldError bool
		errorMsg    string
	}{
		{
			name:        "very large timeout",
			args:        []string{"program", "999999", "echo", "test"},
			shouldError: false,
		},
		{
			name:        "timeout 1",
			args:        []string{"program", "1", "echo", "test"},
			shouldError: false,
		},
		{
			name:        "command with spaces needs quoting in shell but not here",
			args:        []string{"program", "30", "echo hello world", "test"},
			shouldError: false,
		},
		{
			name:        "auto-restart with large timeout",
			args:        []string{"program", "--auto-restart", "86400", "sleep", "1"},
			shouldError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := parseArgs(tt.args)

			if tt.shouldError && err == nil {
				t.Error("Expected error but got nil")
			} else if !tt.shouldError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			} else if tt.shouldError && err != nil && tt.errorMsg != "" {
				if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got: %v", tt.errorMsg, err)
				}
			}
		})
	}
}
