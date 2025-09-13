package timeoutport

import (
	"time"

	commandport "github.com/chitacloud/timeout-mcp/ports/command-port"
	jsonrpcentities "github.com/chitacloud/timeout-mcp/ports/jsonrpc/entities"
)

// TimeoutProxy defines the port interface for timeout functionality
type TimeoutProxy interface {
	// Run starts the proxy operation and processes messages from stdin
	Run() error

	// HandleMessage processes a single JSON-RPC message with timeout logic
	HandleMessage(msg jsonrpcentities.JSONRPCMessage) error

	// Close cleans up proxy resources
	Close() error

	// GetTimeout returns the configured timeout duration
	GetTimeout() time.Duration
}

// TimeoutProxyFactory creates instances of TimeoutProxy
type TimeoutProxyFactory interface {
	// NewTimeoutProxy creates a new timeout proxy instance with a command port
	NewTimeoutProxy(timeout time.Duration, autoRestart bool, commandPort commandport.CommandPort) (TimeoutProxy, error)
}
