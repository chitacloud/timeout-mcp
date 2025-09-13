package defaulttimeoutproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
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
	commandPort  commandport.CommandPort
	pendingCalls map[any]chan jsonrpcentities.JSONRPCMessage
	mu           sync.RWMutex
	autoRestart  bool
	stopReader   chan struct{}
	readerDone   chan struct{}
	initialized  bool                            // Track if MCP server has been initialized before
	initMessage  *jsonrpcentities.JSONRPCMessage // Store the original initialize message
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
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
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
	// Track if we've seen an initialize message and store it for restart
	if msg.Method == "initialize" {
		p.initialized = true
		// Store a copy of the original initialize message for restart
		msgCopy := msg
		p.initMessage = &msgCopy
	}

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

	// Lock to prevent race conditions with Close()
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stop the current reader goroutine
	if p.stopReader != nil {
		select {
		case <-p.stopReader:
			// Already closed
		default:
			close(p.stopReader)
		}

		// Wait for reader to stop
		if p.readerDone != nil {
			// Release lock temporarily to avoid deadlock
			p.mu.Unlock()
			select {
			case <-p.readerDone:
			case <-time.After(500 * time.Millisecond):
				// Timeout waiting for reader to stop
			}
			p.mu.Lock()
		}
	}

	if err := p.commandPort.Stop(); err != nil {
		log.Printf("Warning: Failed to stop command during restart: %v", err)
	}
	
	for id := range p.pendingCalls {
		delete(p.pendingCalls, id)
	}
	
	if err := p.commandPort.Start(); err != nil {
		log.Printf("Error: Failed to restart MCP server: %v", err)
		return
	}

	// Create new channels for the new reader goroutine
	p.stopReader = make(chan struct{})
	p.readerDone = make(chan struct{})

	// Start new reader goroutine
	go p.readTargetResponses()

	// If we were previously initialized, automatically re-initialize the restarted MCP server
	if p.initialized {
		p.sendAutoInitializeAndWait()
	}

	log.Printf("MCP server restarted successfully")
}

// sendAutoInitializeAndWait resends the exact initialization request and waits for response
func (p *DefaultTimeoutProxy) sendAutoInitializeAndWait() {
	if p.initMessage == nil {
		log.Printf("Warning: No stored initialization message to resend")
		return
	}

	log.Printf("Auto-initializing restarted MCP server with original client request...")

	// Create a temporary response channel for the initialization
	responseChan := make(chan jsonrpcentities.JSONRPCMessage, 1)

	p.mu.Lock()
	p.pendingCalls[p.initMessage.ID] = responseChan
	p.mu.Unlock()

	// Send the exact original initialize message (keeping same ID for session continuity)
	if err := p.forwardMessage(*p.initMessage); err != nil {
		log.Printf("Warning: Failed to auto-initialize restarted MCP server: %v", err)
		p.mu.Lock()
		delete(p.pendingCalls, p.initMessage.ID)
		p.mu.Unlock()
		return
	}

	// Wait for the initialization response with a timeout
	select {
	case resp := <-responseChan:
		p.mu.Lock()
		delete(p.pendingCalls, p.initMessage.ID)
		p.mu.Unlock()

		if resp.Error != nil {
			log.Printf("Warning: Auto-initialization failed with error: %v", resp.Error.Message)
		} else {
			log.Printf("Auto-initialization completed successfully")
		}
	case <-time.After(5 * time.Second):
		// Timeout waiting for initialization response
		p.mu.Lock()
		delete(p.pendingCalls, p.initMessage.ID)
		p.mu.Unlock()
		log.Printf("Warning: Auto-initialization timed out after 5 seconds")
	}
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
	defer func() {
		if p.readerDone != nil {
			close(p.readerDone)
		}
	}()

	for {
		if p.stopReader != nil {
			select {
			case <-p.stopReader:
				return
			default:
			}
		}

		stdout := p.commandPort.GetStdout()
		if stdout == nil {
			// Stdout not ready yet, wait a bit
			time.Sleep(10 * time.Millisecond)
			continue
		}

		scanner := bufio.NewScanner(stdout)
		// Increase buffer size to handle large JSON responses (e.g., from file operations)
		maxTokenSize := 16 * 1024 * 1024 // 16MB max token size
		buf := make([]byte, maxTokenSize)
		scanner.Buffer(buf, maxTokenSize)

		for scanner.Scan() {
			if p.stopReader != nil {
				select {
				case <-p.stopReader:
					return
				default:
				}
			}

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
			// Don't spam logs for expected closed pipe errors during restart
			if !strings.Contains(err.Error(), "closed pipe") && !strings.Contains(err.Error(), "file already closed") {
				log.Printf("Error reading from target stdout: %v", err)
			}
			// If it's a closed pipe error, likely a restart occurred - stop this reader
			return
		}

		// If we reach here, the scanner stopped (likely due to closed stdout after restart)
		// Wait a bit before trying again, but check for stop signal first
		if p.stopReader != nil {
			select {
			case <-p.stopReader:
				return
			case <-time.After(100 * time.Millisecond):
				// Continue to next iteration
			}
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// Close stops the proxy and cleans up resources
func (p *DefaultTimeoutProxy) Close() error {
	p.mu.Lock()
	stopReader := p.stopReader
	readerDone := p.readerDone
	p.mu.Unlock()

	// Stop the reader goroutine
	if stopReader != nil {
		select {
		case <-stopReader:
			// Already closed
		default:
			close(stopReader)
		}
	}

	// Wait for the reader to finish (with timeout to avoid hanging)
	if readerDone != nil {
		select {
		case <-readerDone:
			// Reader finished cleanly
		case <-time.After(1 * time.Second):
			// Timeout waiting for reader, continue with cleanup
		}
	}

	return p.commandPort.Stop()
}
