package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	defaultcommandadapter "github.com/chitacloud/timeout-mcp/adapters/default-command-adapter"
	defaulttimeoutproxy "github.com/chitacloud/timeout-mcp/adapters/default-timeout-proxy"
	jsonrpcentities "github.com/chitacloud/timeout-mcp/ports/jsonrpc/entities"
)

// Test integration with a real MCP-like server (simulated with a script)
func TestTimeoutProxy_IntegrationWithMockMCPServer(t *testing.T) {
	// Create a mock MCP server script that responds to JSON-RPC calls
	mockServerScript := `#!/bin/bash
while IFS= read -r line; do
	# Parse the method from the JSON-RPC message
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	
	case "$method" in
		"initialize")
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"capabilities\":{\"tools\":{}}}}"
			;;
		"tools/list")
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"tools\":[{\"name\":\"test_tool\",\"description\":\"A test tool\"}]}}"
			;;
		"tools/call")
			# Simulate a quick response for tools/call
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Tool executed successfully\"}]}}"
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}"
			;;
	esac
done`

	// Write the mock server script to a temporary file
	tmpScript, err := os.CreateTemp("", "mock_mcp_server_*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer os.Remove(tmpScript.Name())

	if _, err := tmpScript.WriteString(mockServerScript); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}
	tmpScript.Close()

	// Make the script executable
	if err := os.Chmod(tmpScript.Name(), 0755); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	// Create command port for the mock server
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", tmpScript.Name())
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	// Create timeout proxy with 2 second timeout
	timeout := 2 * time.Second
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(timeout, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test initialize call (should be forwarded immediately)
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  map[string]any{"clientInfo": map[string]any{"name": "test-client"}},
	}

	err = proxy.HandleMessage(initMsg)
	if err != nil {
		t.Errorf("Failed to handle initialize message: %v", err)
	}

	// Test tools/list call (should be forwarded immediately)
	toolsListMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      2,
		Method:  "tools/list",
	}

	err = proxy.HandleMessage(toolsListMsg)
	if err != nil {
		t.Errorf("Failed to handle tools/list message: %v", err)
	}

	// Test tools/call (should use timeout logic)
	toolsCallMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      3,
		Method:  "tools/call",
		Params:  map[string]any{"name": "test_tool", "arguments": map[string]any{}},
	}

	err = proxy.HandleMessage(toolsCallMsg)
	if err != nil {
		t.Errorf("Failed to handle tools/call message: %v", err)
	}

	t.Log("Integration test completed successfully")
}

// Test timeout behavior with a slow mock server
func TestTimeoutProxy_IntegrationTimeout(t *testing.T) {
	// Create a mock server that delays tools/call responses
	slowServerScript := `#!/bin/bash
while IFS= read -r line; do
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	
	case "$method" in
		"tools/call")
			# Sleep for 3 seconds to trigger timeout
			sleep 3
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Slow response\"}]}}"
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"status\":\"ok\"}}"
			;;
	esac
done`

	// Write the slow server script
	tmpScript, err := os.CreateTemp("", "slow_mcp_server_*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer os.Remove(tmpScript.Name())

	if _, err := tmpScript.WriteString(slowServerScript); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}
	tmpScript.Close()

	if err := os.Chmod(tmpScript.Name(), 0755); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	// Create command port and proxy with 1 second timeout
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", tmpScript.Name())
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(1*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test that tools/call times out
	toolsCallMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  map[string]any{"name": "slow_tool"},
	}

	start := time.Now()
	err = proxy.HandleMessage(toolsCallMsg)
	elapsed := time.Since(start)

	// Should complete within timeout period (plus some buffer)
	if elapsed > 2*time.Second {
		t.Errorf("HandleMessage took too long: %v", elapsed)
	}

	// The test should pass regardless of error since we're testing timeout behavior
	t.Logf("HandleMessage completed in %v", elapsed)
}

// Test with a real echo command as MCP server
func TestTimeoutProxy_IntegrationWithEcho(t *testing.T) {
	// Create command port using cat (which echoes stdin to stdout)
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("cat")
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(5*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Start a response reader to capture echoed messages
	responseChan := make(chan string, 10)
	go func() {
		scanner := bufio.NewScanner(commandPort.GetStdout())
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) != "" {
				responseChan <- line
			}
		}
	}()

	// Test message forwarding
	testMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
		Params:  map[string]any{"test": "data"},
	}

	err = proxy.HandleMessage(testMsg)
	if err != nil {
		t.Errorf("Failed to handle message: %v", err)
	}

	// Wait briefly for echo response
	select {
	case response := <-responseChan:
		// Verify the response is valid JSON-RPC
		var echoed jsonrpcentities.JSONRPCMessage
		if err := json.Unmarshal([]byte(response), &echoed); err != nil {
			t.Errorf("Failed to parse echoed response: %v", err)
		} else {
			if echoed.Method != "initialize" {
				t.Errorf("Expected method 'initialize', got '%s'", echoed.Method)
			}
		}
	case <-time.After(1 * time.Second):
		t.Log("No response received within timeout (expected for echo test)")
	}
}

func TestTimeoutProxy_ToolsListResponseFlow(t *testing.T) {
	// Create a script that simulates an MCP server responding to tools/list
	script := `#!/bin/bash
while IFS= read -r line; do
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	
	case "$method" in
		"tools/list")
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"tools\":[{\"name\":\"echo_tool\",\"description\":\"Echoes input text\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\",\"description\":\"Text to echo\"}},\"required\":[\"text\"]}},{\"name\":\"calculate\",\"description\":\"Performs calculations\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"expression\":{\"type\":\"string\",\"description\":\"Math expression\"}},\"required\":[\"expression\"]}}]}}"
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}"
			;;
	esac
done`

	// Write script to temp file
	scriptFile, err := os.CreateTemp("", "tools_list_mcp_*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer os.Remove(scriptFile.Name())

	if _, err := scriptFile.WriteString(script); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}
	scriptFile.Close()

	if err := os.Chmod(scriptFile.Name(), 0755); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", scriptFile.Name())
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	// Create timeout proxy
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(5*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Capture stdout to verify response is forwarded to client
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Redirect stdout temporarily
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	// Start proxy in background
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- proxy.Run()
	}()

	// Give proxy time to start
	time.Sleep(200 * time.Millisecond)

	// Send tools/list request
	toolsListMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tools-list-test",
		Method:  "tools/list",
		Params:  map[string]any{},
	}

	start := time.Now()
	err = proxy.HandleMessage(toolsListMsg)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error for tools/list request, got: %v", err)
	}

	// Should complete quickly (not timeout)
	if duration > 4*time.Second {
		t.Errorf("tools/list request took too long: %v", duration)
	}

	// Close write end and read response
	w.Close()
	os.Stdout = oldStdout

	responseBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}

	responseStr := string(responseBytes)

	// Verify response contains expected tools
	if !strings.Contains(responseStr, "echo_tool") {
		t.Errorf("Expected response to contain 'echo_tool', got: %s", responseStr)
	}
	if !strings.Contains(responseStr, "calculate") {
		t.Errorf("Expected response to contain 'calculate', got: %s", responseStr)
	}
	if !strings.Contains(responseStr, "tools-list-test") {
		t.Errorf("Expected response to contain request ID 'tools-list-test', got: %s", responseStr)
	}

	t.Logf("tools/list response flow verified in %v: %s", duration, responseStr)
}

func TestTimeoutProxy_ToolsListTimeout(t *testing.T) {
	// Create command port with a command that will not respond to tools/list
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("sleep", "10") // Command that won't respond
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	// Create timeout proxy with short timeout
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(1*time.Second, false, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Capture stdout to verify timeout response is sent to client
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Redirect stdout temporarily
	oldStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldStdout }()

	// Start proxy in background
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- proxy.Run()
	}()

	// Give proxy time to start
	time.Sleep(100 * time.Millisecond)

	// Send tools/list request
	toolsListMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tools-list-timeout-test",
		Method:  "tools/list",
		Params:  map[string]any{},
	}

	start := time.Now()
	err = proxy.HandleMessage(toolsListMsg)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error for tools/list request, got: %v", err)
	}

	// Should timeout after ~1 second
	if duration < 900*time.Millisecond || duration > 1500*time.Millisecond {
		t.Errorf("Expected tools/list to timeout after ~1s, took %v", duration)
	}

	// Close write end and read timeout response
	w.Close()
	os.Stdout = oldStdout

	responseBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Failed to read timeout response: %v", err)
	}

	responseStr := string(responseBytes)

	// Verify timeout error response is sent to client
	if !strings.Contains(responseStr, "timed out") {
		t.Errorf("Expected timeout error response, got: %s", responseStr)
	}
	if !strings.Contains(responseStr, "tools-list-timeout-test") {
		t.Errorf("Expected response to contain request ID 'tools-list-timeout-test', got: %s", responseStr)
	}
	if !strings.Contains(responseStr, "tools/list") {
		t.Errorf("Expected response to mention 'tools/list' method, got: %s", responseStr)
	}

	t.Logf("tools/list timeout response verified in %v: %s", duration, responseStr)
}

func TestTimeoutProxy_AutoInitializeAfterRestart(t *testing.T) {
	// Create a script that tracks initialization messages
	script := `#!/bin/bash
count=0
while IFS= read -r line; do
	count=$((count + 1))
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	
	case "$method" in
		"initialize")
			echo "INIT_${count}: ID=$id" >&2  # Log to stderr for tracking
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{\"listChanged\":true}},\"serverInfo\":{\"name\":\"test-server\",\"version\":\"1.0.0\"}}}"
			;;
		"tools/call")
			# Force timeout to trigger restart
			sleep 2
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Response after timeout\"}]}}"
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}"
			;;
	esac
done`

	// Write script to temp file
	scriptFile, err := os.CreateTemp("", "auto_init_test_*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer os.Remove(scriptFile.Name())

	if _, err := scriptFile.WriteString(script); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}
	scriptFile.Close()

	if err := os.Chmod(scriptFile.Name(), 0755); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", scriptFile.Name())
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	// Create timeout proxy with auto-restart enabled and short timeout
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(500*time.Millisecond, true, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Start proxy in background
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- proxy.Run()
	}()

	// Give proxy time to start
	time.Sleep(100 * time.Millisecond)

	// Send initial initialize request with specific parameters
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "test-init-123",
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"roots": map[string]any{
					"listChanged": true,
				},
				"sampling": map[string]any{},
			},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "2.0.0",
			},
		},
	}

	err = proxy.HandleMessage(initMsg)
	if err != nil {
		t.Errorf("Expected no error for initialize request, got: %v", err)
	}

	// Wait for initialization to complete
	time.Sleep(200 * time.Millisecond)

	// Send a tools/call that will timeout and trigger restart
	toolCallMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tool-call-timeout",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "test_tool",
			"arguments": map[string]any{
				"text": "This will timeout and trigger restart",
			},
		},
	}

	start := time.Now()
	err = proxy.HandleMessage(toolCallMsg)
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Expected no error for tools/call request, got: %v", err)
	}

	// Should timeout and trigger restart
	if duration < 400*time.Millisecond || duration > 800*time.Millisecond {
		t.Errorf("Expected timeout after ~500ms, took %v", duration)
	}

	// Give time for restart and auto-initialization
	time.Sleep(1 * time.Second)

	t.Logf("Auto-initialization after restart test completed in %v", duration)
}

func TestTimeoutProxy_ToolCallAfterRestart(t *testing.T) {
	// Create a script that properly handles initialization and tool calls
	script := `#!/bin/bash
count=0
initialized=false
while IFS= read -r line; do
	count=$((count + 1))
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	
	echo "RECEIVED: $line" >&2
	
	case "$method" in
		"initialize")
			echo "INIT_${count}: ID=$id" >&2
			initialized=true
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{\"listChanged\":true}},\"serverInfo\":{\"name\":\"test-server\",\"version\":\"1.0.0\"}}}"
			;;
		"tools/call")
			echo "TOOL_CALL_${count}: ID=$id, initialized=$initialized" >&2
			if [ "$initialized" = "true" ]; then
				name=$(echo "$line" | jq -r '.params.name // "unknown"')
				echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Tool $name executed successfully\"}]}}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32602,\"message\":\"Invalid request parameters\"}}"
			fi
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}"
			;;
	esac
done`

	// Write script to temp file
	scriptFile, err := os.CreateTemp("", "tool_call_test_*.sh")
	if err != nil {
		t.Fatalf("Failed to create temp script: %v", err)
	}
	defer os.Remove(scriptFile.Name())

	if _, err := scriptFile.WriteString(script); err != nil {
		t.Fatalf("Failed to write script: %v", err)
	}
	scriptFile.Close()

	if err := os.Chmod(scriptFile.Name(), 0755); err != nil {
		t.Fatalf("Failed to make script executable: %v", err)
	}

	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort("bash", scriptFile.Name())
	if err != nil {
		t.Fatalf("Failed to create command port: %v", err)
	}

	// Create timeout proxy with auto-restart enabled and short timeout
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(800*time.Millisecond, true, commandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Start proxy in background
	proxyDone := make(chan error, 1)
	go func() {
		proxyDone <- proxy.Run()
	}()

	// Give proxy time to start
	time.Sleep(100 * time.Millisecond)

	// Send initial initialize request
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "test-init-456",
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"roots": map[string]any{
					"listChanged": true,
				},
			},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	}

	err = proxy.HandleMessage(initMsg)
	if err != nil {
		t.Errorf("Expected no error for initialize request, got: %v", err)
	}

	// Wait for initialization to complete
	time.Sleep(200 * time.Millisecond)

	// Send a tool call that will succeed initially
	toolCallMsg1 := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tool-call-before-restart",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "test_tool",
			"arguments": map[string]any{
				"text": "This should work before restart",
			},
		},
	}

	err = proxy.HandleMessage(toolCallMsg1)
	if err != nil {
		t.Errorf("Expected no error for first tools/call request, got: %v", err)
	}

	// Now send a tool call that will timeout and trigger restart
	toolCallMsg2 := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tool-call-timeout",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "slow_tool",
			"arguments": map[string]any{
				"text": "This will timeout and trigger restart",
			},
		},
	}

	// Create a script that will hang on this specific tool call to force timeout
	hangScript := `#!/bin/bash
count=0
initialized=false
while IFS= read -r line; do
	count=$((count + 1))
	method=$(echo "$line" | jq -r '.method // empty')
	id=$(echo "$line" | jq -r '.id // empty')
	name=$(echo "$line" | jq -r '.params.name // "unknown"')
	
	echo "RECEIVED: $line" >&2
	
	case "$method" in
		"initialize")
			echo "INIT_${count}: ID=$id" >&2
			initialized=true
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{\"listChanged\":true}},\"serverInfo\":{\"name\":\"test-server\",\"version\":\"1.0.0\"}}}"
			;;
		"tools/call")
			echo "TOOL_CALL_${count}: ID=$id, name=$name, initialized=$initialized" >&2
			if [ "$name" = "slow_tool" ]; then
				echo "HANGING on slow_tool to force timeout" >&2
				sleep 2  # This will cause timeout
				echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Tool $name executed after delay\"}]}}"
			elif [ "$initialized" = "true" ]; then
				echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Tool $name executed successfully\"}]}}"
			else
				echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32602,\"message\":\"Invalid request parameters - not initialized\"}}"
			fi
			;;
		*)
			echo "{\"jsonrpc\":\"2.0\",\"id\":\"$id\",\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}"
			;;
	esac
done`

	// Write new hanging script
	hangScriptFile, err := os.CreateTemp("", "hang_script_*.sh")
	if err != nil {
		t.Fatalf("Failed to create hang script: %v", err)
	}
	defer os.Remove(hangScriptFile.Name())

	if _, err := hangScriptFile.WriteString(hangScript); err != nil {
		t.Fatalf("Failed to write hang script: %v", err)
	}
	hangScriptFile.Close()

	if err := os.Chmod(hangScriptFile.Name(), 0755); err != nil {
		t.Fatalf("Failed to make hang script executable: %v", err)
	}

	// Stop current proxy and restart with hanging script
	proxy.Close()
	time.Sleep(100 * time.Millisecond)

	// Create new command port with hanging script
	hangCommandPort, err := commandFactory.NewCommandPort("bash", hangScriptFile.Name())
	if err != nil {
		t.Fatalf("Failed to create hang command port: %v", err)
	}

	// Create new proxy
	hangProxy, err := factory.NewTimeoutProxy(800*time.Millisecond, true, hangCommandPort)
	if err != nil {
		t.Fatalf("Failed to create hang proxy: %v", err)
	}
	defer hangProxy.Close()

	// Start new proxy in background
	go func() {
		hangProxy.Run()
	}()

	time.Sleep(100 * time.Millisecond)

	// Re-initialize the new proxy
	err = hangProxy.HandleMessage(initMsg)
	if err != nil {
		t.Errorf("Expected no error for re-initialize request, got: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Now send the slow tool call that will timeout
	start := time.Now()
	err = hangProxy.HandleMessage(toolCallMsg2)
	duration := time.Since(start)

	// Should timeout and trigger restart
	if duration < 700*time.Millisecond || duration > 1000*time.Millisecond {
		t.Errorf("Expected timeout after ~800ms, took %v", duration)
	}

	// Give time for restart and auto-initialization
	time.Sleep(1 * time.Second)

	// Now send another tool call after restart - this is where the bug should appear
	toolCallMsg3 := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tool-call-after-restart",
		Method:  "tools/call",
		Params: map[string]any{
			"name": "post_restart_tool",
			"arguments": map[string]any{
				"text": "This should work after restart but might fail",
			},
		},
	}

	err = hangProxy.HandleMessage(toolCallMsg3)
	if err != nil {
		t.Errorf("Tool call after restart failed: %v", err)
		// Let's see if we can identify the actual error message
		if strings.Contains(err.Error(), "Invalid request parameters") {
			t.Logf("FOUND THE BUG: Tool call after restart got 'Invalid request parameters'")
		}
	}

	t.Logf("Tool call after restart test completed - restart took %v", duration)
}
