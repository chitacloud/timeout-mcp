package defaulttimeoutproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	commandport "github.com/chitacloud/timeout-mcp/ports/command-port"
	jsonrpcentities "github.com/chitacloud/timeout-mcp/ports/jsonrpc/entities"
	timeoutport "github.com/chitacloud/timeout-mcp/ports/timeout-port"
)

var (
	_ timeoutport.TimeoutProxy = (*DefaultTimeoutProxy)(nil)
)

// DefaultTimeoutProxy implements TimeoutProxy interface
type DefaultTimeoutProxy struct {
	timeout      time.Duration
	autoRestart  bool
	commandPort  commandport.CommandPort
	pendingCalls map[interface{}]chan jsonrpcentities.JSONRPCMessage
	mu           sync.RWMutex
}

// DefaultTimeoutProxyFactory implements TimeoutProxyFactory
type DefaultTimeoutProxyFactory struct{}

// NewTimeoutProxy creates a new DefaultTimeoutProxy instance
func (f *DefaultTimeoutProxyFactory) NewTimeoutProxy(timeout time.Duration, autoRestart bool, commandPort commandport.CommandPort) (timeoutport.TimeoutProxy, error) {
	// Start the command
	if err := commandPort.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	return &DefaultTimeoutProxy{
		timeout:      timeout,
		autoRestart:  autoRestart,
		commandPort:  commandPort,
		pendingCalls: make(map[interface{}]chan jsonrpcentities.JSONRPCMessage),
	}, nil
}

// GetTimeout returns the configured timeout duration
func (p *DefaultTimeoutProxy) GetTimeout() time.Duration {
	return p.timeout
}

// Run starts the proxy operation
func (p *DefaultTimeoutProxy) Run() error {
	// Start goroutine to read from target stdout and forward responses
	go p.readTargetResponses()

	// Read from client stdin and forward requests
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg jsonrpcentities.JSONRPCMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("Failed to parse JSON-RPC message: %v", err)
			continue
		}

		if err := p.HandleMessage(msg); err != nil {
			log.Printf("Failed to handle message: %v", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading from stdin: %w", err)
	}

	return nil
}

// HandleMessage processes a single JSON-RPC message
func (p *DefaultTimeoutProxy) HandleMessage(msg jsonrpcentities.JSONRPCMessage) error {
	// Check if this is a method that needs timeout handling
	if p.shouldApplyTimeout(msg.Method) && msg.ID != nil {
		return p.handleTimeoutMethod(msg)
	}

	// For all other messages, forward directly
	return p.forwardMessage(msg)
}

// shouldApplyTimeout determines if a method should have timeout applied
func (p *DefaultTimeoutProxy) shouldApplyTimeout(method string) bool {
	switch method {
	case "tools/call", "initialize", "tools/list":
		return true
	default:
		return false
	}
}

// handleTimeoutMethod processes any method that requires timeout handling
func (p *DefaultTimeoutProxy) handleTimeoutMethod(msg jsonrpcentities.JSONRPCMessage) error {
	// Create a channel to receive the response
	responseChan := make(chan jsonrpcentities.JSONRPCMessage, 1)

	p.mu.Lock()
	p.pendingCalls[msg.ID] = responseChan
	p.mu.Unlock()

	// Forward the message to the target
	if err := p.forwardMessage(msg); err != nil {
		p.mu.Lock()
		delete(p.pendingCalls, msg.ID)
		p.mu.Unlock()
		return err
	}

	// Wait for response or timeout
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	select {
	case response := <-responseChan:
		// Forward the response to client
		return p.sendToClient(response)

	case <-ctx.Done():
		// Timeout occurred - remove from pending calls and send error
		p.mu.Lock()
		delete(p.pendingCalls, msg.ID)
		p.mu.Unlock()

		// Send timeout error response
		var errorMessage string
		if p.autoRestart {
			errorMessage = fmt.Sprintf("Method '%s' timed out after %s (restarting now...)", msg.Method, p.timeout.String())
			// Trigger restart in background
			go p.restartCommand()
		} else {
			errorMessage = fmt.Sprintf("Method '%s' timed out after %s", msg.Method, p.timeout.String())
		}
		
		errorResponse := jsonrpcentities.JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error: &jsonrpcentities.JSONRPCError{
				Code:    -32603, // Internal error code
				Message: errorMessage,
			},
		}
		return p.sendToClient(errorResponse)
	}
}

// restartCommand restarts the underlying MCP server process
func (p *DefaultTimeoutProxy) restartCommand() {
	log.Printf("Auto-restarting MCP server due to timeout...")
	
	// Stop the current command
	if err := p.commandPort.Stop(); err != nil {
		log.Printf("Warning: Failed to stop command during restart: %v", err)
	}
	
	// Clear all pending calls since they're invalid after restart
	p.mu.Lock()
	for id := range p.pendingCalls {
		delete(p.pendingCalls, id)
	}
	p.mu.Unlock()
	
	// Start the command again
	if err := p.commandPort.Start(); err != nil {
		log.Printf("Error: Failed to restart MCP server: %v", err)
		return
	}
	
	log.Printf("MCP server restarted successfully")
}

// forwardMessage sends a message to the target MCP server
func (p *DefaultTimeoutProxy) forwardMessage(msg jsonrpcentities.JSONRPCMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	data = append(data, '\n')
	_, err = p.commandPort.GetStdin().Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to target stdin: %w", err)
	}

	return nil
}

// sendToClient sends a message to the client
func (p *DefaultTimeoutProxy) sendToClient(msg jsonrpcentities.JSONRPCMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	data = append(data, '\n')
	_, err = os.Stdout.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to stdout: %w", err)
	}

	return nil
}

// readTargetResponses reads responses from the target MCP server
func (p *DefaultTimeoutProxy) readTargetResponses() {

	scanner := bufio.NewScanner(p.commandPort.GetStdout())
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg jsonrpcentities.JSONRPCMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("Failed to parse target response: %v", err)
			continue
		}

		// Check if this is a response to a pending tool call
		if msg.ID != nil {
			p.mu.RLock()
			responseChan, exists := p.pendingCalls[msg.ID]
			p.mu.RUnlock()

			if exists {
				// This is a response to a tool call - send it through the channel
				select {
				case responseChan <- msg:
					// Response sent successfully - don't delete here, let handleToolCall do it
				default:
					// Channel might be closed due to timeout, forward directly
					p.sendToClient(msg)
				}
				continue
			}
		}

		// For all other messages (notifications, other responses), forward directly
		if err := p.sendToClient(msg); err != nil {
			log.Printf("Failed to forward response: %v", err)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		log.Printf("Error reading from target stdout: %v", err)
	}
}

// Close cleans up the proxy resources
func (p *DefaultTimeoutProxy) Close() error {
	return p.commandPort.Stop()
}
