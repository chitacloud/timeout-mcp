package defaultcommandadapter

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"

	commandport "github.com/chitacloud/timeout-mcp/ports/command-port"
)

var (
	_ commandport.CommandPort        = (*DefaultCommandAdapter)(nil)
	_ commandport.CommandPortFactory = (*DefaultCommandAdapterFactory)(nil)
)

// DefaultCommandAdapter implements CommandPort using os/exec
type DefaultCommandAdapter struct {
	command string
	args    []string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
}

// DefaultCommandAdapterFactory implements CommandPortFactory
type DefaultCommandAdapterFactory struct{}

// NewCommandPort creates a new DefaultCommandAdapter
func (f *DefaultCommandAdapterFactory) NewCommandPort(command string, args ...string) (commandport.CommandPort, error) {
	if command == "" {
		return nil, fmt.Errorf("command cannot be empty")
	}

	return &DefaultCommandAdapter{
		command: command,
		args:    args,
	}, nil
}

// Start starts the command process
func (c *DefaultCommandAdapter) Start() error {
	// Check if command is already running
	if c.IsRunning() {
		return fmt.Errorf("command already started")
	}

	// Create a new Cmd instance (required for restart after Stop)
	c.cmd = exec.Command(c.command, c.args...)

	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return err
	}
	c.stdin = stdin

	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		c.stdin.Close()
		return err
	}
	c.stdout = stdout

	return c.cmd.Start()
}

// Stop stops the command process
func (c *DefaultCommandAdapter) Stop() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return fmt.Errorf("command not started")
	}

	if c.stdin != nil {
		c.stdin.Close()
		c.stdin = nil
	}
	if c.stdout != nil {
		c.stdout.Close()
		c.stdout = nil
	}

	err := c.cmd.Process.Kill()
	if err != nil {
		return err
	}

	// Wait for the process to finish - ignore error as process was killed
	c.cmd.Wait()

	// Clear the cmd reference to allow restart
	c.cmd = nil
	return nil
}

// GetStdin returns the stdin writer for the command
func (c *DefaultCommandAdapter) GetStdin() io.WriteCloser {
	return c.stdin
}

// GetStdout returns the stdout reader for the command
func (c *DefaultCommandAdapter) GetStdout() io.ReadCloser {
	return c.stdout
}

// IsRunning returns true if the command is currently running
func (c *DefaultCommandAdapter) IsRunning() bool {
	if c.cmd == nil || c.cmd.Process == nil {
		return false
	}

	// If ProcessState is available and process has exited, it's not running
	if c.cmd.ProcessState != nil && c.cmd.ProcessState.Exited() {
		return false
	}

	// Check if process is still alive by sending signal 0
	// Signal 0 is used to check if process exists without actually sending a signal
	err := c.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}
