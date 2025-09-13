package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	defaultcommandadapter "github.com/chitacloud/timeout-mcp/adapters/default-command-adapter"
	defaulttimeoutproxy "github.com/chitacloud/timeout-mcp/adapters/default-timeout-proxy"
)

// parseArgs validates and parses command line arguments
func parseArgs(args []string) (timeout time.Duration, autoRestart bool, command string, targetArgs []string, err error) {
	if len(args) < 3 {
		err = fmt.Errorf("insufficient arguments: expected at least 3, got %d", len(args))
		return
	}

	argIndex := 1
	
	// Check for --auto-restart flag
	if args[argIndex] == "--auto-restart" {
		autoRestart = true
		argIndex++
		if len(args) < argIndex+2 {
			err = fmt.Errorf("insufficient arguments after --auto-restart: expected at least %d more, got %d", 2, len(args)-argIndex)
			return
		}
	}

	// Parse timeout duration
	timeoutSeconds, parseErr := strconv.Atoi(args[argIndex])
	if parseErr != nil {
		err = fmt.Errorf("invalid timeout value: %v", parseErr)
		return
	}
	
	if timeoutSeconds <= 0 {
		err = fmt.Errorf("invalid timeout value: must be positive, got %d", timeoutSeconds)
		return
	}
	
	timeout = time.Duration(timeoutSeconds) * time.Second
	command = args[argIndex+1]
	targetArgs = args[argIndex+2:]
	return
}

// runProxy creates and runs the timeout proxy with the given parameters
func runProxy(timeout time.Duration, autoRestart bool, command string, targetArgs []string) error {
	// Create command port
	commandFactory := &defaultcommandadapter.DefaultCommandAdapterFactory{}
	commandPort, err := commandFactory.NewCommandPort(command, targetArgs...)
	if err != nil {
		return fmt.Errorf("failed to create command port: %v", err)
	}

	// Create factory and proxy using the new architecture
	factory := &defaulttimeoutproxy.DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(timeout, autoRestart, commandPort)
	if err != nil {
		return fmt.Errorf("failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Start the proxy
	if err := proxy.Run(); err != nil {
		return fmt.Errorf("proxy error: %v", err)
	}
	return nil
}

func main() {
	timeout, autoRestart, command, targetArgs, err := parseArgs(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Usage: %s [--auto-restart] <timeout_seconds> <target_command> [target_args...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s 30 npx -y some-mcp-server\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s --auto-restart 30 npx -y some-mcp-server\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := runProxy(timeout, autoRestart, command, targetArgs); err != nil {
		log.Fatalf("Failed to run proxy: %v", err)
	}
}
