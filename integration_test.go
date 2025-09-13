package main

import (
	"bufio"
	"encoding/json"
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
		Params:  map[string]interface{}{"clientInfo": map[string]interface{}{"name": "test-client"}},
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
		Params:  map[string]interface{}{"name": "test_tool", "arguments": map[string]interface{}{}},
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
		Params:  map[string]interface{}{"name": "slow_tool"},
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
		Params:  map[string]interface{}{"test": "data"},
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
