package commandport

import (
	"io"
)

// CommandPort defines the interface for executing and managing external commands
type CommandPort interface {
	// Start starts the command process
	Start() error
	
	// Stop stops the command process
	Stop() error
	
	// GetStdin returns the stdin writer for the command
	GetStdin() io.WriteCloser
	
	// GetStdout returns the stdout reader for the command  
	GetStdout() io.ReadCloser
	
	// IsRunning returns true if the command is currently running
	IsRunning() bool
}

// CommandPortFactory creates instances of CommandPort
type CommandPortFactory interface {
	// NewCommandPort creates a new command port for the given command and arguments
	NewCommandPort(command string, args ...string) (CommandPort, error)
}
