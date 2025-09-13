# Timeout MCP Wrapper

A Go-based wrapper for Model Context Protocol (MCP) servers that adds configurable timeout functionality to critical MCP methods (`tools/call`, `initialize`, `tools/list`) while maintaining transparent forwarding for all other JSON-RPC operations.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Report Card](https://goreportcard.com/badge/github.com/chitacloud/timeout-mcp)](https://goreportcard.com/report/github.com/chitacloud/timeout-mcp)
[![Test Coverage](https://img.shields.io/badge/coverage-95%25-brightgreen.svg)](https://github.com/chitacloud/timeout-mcp)

## Table of Contents

- [Features](#features)
- [Technologies Used](#technologies-used)
- [Architecture](#architecture)
- [Installation](#installation)
- [Usage](#usage)
- [JSON-RPC Behavior](#json-rpc-behavior)
- [Development](#development)
- [Error Handling](#error-handling)
- [Performance Considerations](#performance-considerations)
- [Contributing](#contributing)
- [Support](#support)
- [Project Status](#project-status)
- [License](#license)
- [Troubleshooting](#troubleshooting)
- [Acknowledgments](#acknowledgments)

## Features

- **Selective Timeout**: Applies timeout logic to critical MCP methods: `tools/call`, `initialize`, and `tools/list`
- **Transparent Forwarding**: All other JSON-RPC methods (notifications, etc.) are forwarded immediately
- **Clean Architecture**: Built using Ports and Adapters pattern for modularity and testability
- **Configurable Timeout**: Command-line configurable timeout duration
- **Process Management**: Handles subprocess lifecycle with proper cleanup

## Technologies Used

- **[Go](https://golang.org/)** (1.23+) - Primary programming language
- **[JSON-RPC 2.0](https://www.jsonrpc.org/specification)** - Protocol specification for MCP communication
- **Standard Library Packages:**
  - `os/exec` - Subprocess management
  - `encoding/json` - JSON parsing and serialization
  - `time` - Timeout and duration handling
  - `sync` - Concurrency synchronization
  - `io` - Input/output operations
- **Testing Framework:**
  - `testing` - Go's built-in testing framework
  - `go.uber.org/mock/gomock` - Mock generation for unit tests

## Architecture

The wrapper follows the Ports and Adapters (Hexagonal) architecture:

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   JSON-RPC      │───▶│  TimeoutProxy    │───▶│   Target MCP    │
│   Client        │    │   (Core Logic)   │    │   Server        │
└─────────────────┘    └──────────────────┘    └─────────────────┘
                              │
                        ┌─────▼─────┐
                        │CommandPort│
                        │(Interface)│
                        └───────────┘
```

### Core Components

- **TimeoutProxy** (Port): Interface defining timeout proxy behavior
- **DefaultTimeoutProxy** (Adapter): Implementation with subprocess management
- **CommandPort** (Port): Interface for subprocess operations
- **DefaultCommandAdapter** (Adapter): Implementation using `os/exec`

## Installation

```bash
go install github.com/chitacloud/timeout-mcp@v0.0.0-rc01
```

## Usage

### Basic Usage

```bash
timeout-mcp [--auto-restart] <duration> <command> [args...]
```

### Arguments

- `<duration>`: Timeout duration in seconds for critical MCP methods (`tools/call`, `initialize`, `tools/list`) (default: 30)
- `--auto-restart`: (Optional) Automatically restart the MCP server when a timeout occurs

### Examples

#### Wrap a Python MCP Server
```bash
timeout-mcp 45 python weather-mcp-server.py --api-key YOUR_KEY
```

#### Wrap a Node.js MCP Server  
```bash
timeout-mcp 60 node filesystem-mcp-server.js --root /safe/directory
```

#### Short Timeout for Testing
```bash
timeout-mcp 5 python slow-mcp-server.py
```

#### Auto-Restart on Timeout
```bash
timeout-mcp --auto-restart 30 python unreliable-mcp-server.py
```

#### Auto-Restart with Custom Timeout
```bash
timeout-mcp --auto-restart 45 uvx weather-mcp --api-key YOUR_KEY
```

## JSON-RPC Behavior

### Methods with Timeout
The following methods have timeout applied:

#### Tool Calls
```jsonc
// Request
{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "get_weather", "arguments": {"city": "San Francisco"}}}

// Success Response (within timeout)
{"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text", "text": "Sunny, 72°F"}]}}

// Timeout Response (after timeout)
{"jsonrpc": "2.0", "id": 1, "error": {"code": -32603, "message": "Method 'tools/call' timed out after 30s"}}
```

#### Initialize
```jsonc
// Request
{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"clientInfo": {"name": "test-client"}}}

// Timeout Response (if server doesn't respond)
{"jsonrpc": "2.0", "id": 1, "error": {"code": -32603, "message": "Method 'initialize' timed out after 30s"}}
```

#### Tools List
```jsonc
// Request
{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}

// Timeout Response (if server doesn't respond)
{"jsonrpc": "2.0", "id": 2, "error": {"code": -32603, "message": "Method 'tools/list' timed out after 30s"}}
```

### Methods without Timeout (immediate forwarding)
```jsonc
// Notifications - forwarded immediately
{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": {"requestId": 1}}

// Resources - forwarded immediately
{"jsonrpc": "2.0", "id": 3, "method": "resources/list"}
```

## Auto-Restart Feature

When the `--auto-restart` flag is enabled, the wrapper automatically restarts the underlying MCP server subprocess whenever a timeout occurs on critical methods (`tools/call`, `initialize`, `tools/list`).

### Behavior

- **Timeout Detection**: When a timeout occurs on a critical method
- **Error Response**: Returns a JSON-RPC error with the suffix `(restarting now...)`
- **Background Restart**: Asynchronously stops and restarts the MCP server subprocess
- **State Cleanup**: Clears any pending calls to avoid stale state
- **Logging**: Emits restart logs for visibility

### Example Error Response with Auto-Restart

```json
// Timeout Response with auto-restart enabled
{"jsonrpc": "2.0", "id": 1, "error": {"code": -32603, "message": "Method 'tools/call' timed out after 30s (restarting now...)"}}
```

### Use Cases

- **Unreliable MCP Servers**: Automatically recover from hanging or crashed servers
- **Development**: Quickly recover from server bugs during testing
- **Production Resilience**: Maintain service availability despite intermittent server issues

### Important Notes

- Auto-restart only triggers on timeout, not on other errors
- The restart process runs in the background to avoid blocking the proxy
- Pending calls are cleared during restart to prevent inconsistent state
- Restart failures are logged but don't crash the wrapper

## Development

### Prerequisites

- Go 1.21 or later
- Basic understanding of JSON-RPC 2.0
- MCP server for testing

### Running Tests

```bash
# Run all tests
go test -v .

# Run specific test categories
go test -v . -run Unit
go test -v . -run Integration
go test -v . -run TimeoutProxy
```

### Project Structure

```
timeout-mcp/
├── main.go                              # Entry point and CLI parsing
├── main_test.go                         # Unit tests
├── integration_test.go                  # Integration tests
├── ports/                               # Port interfaces
│   ├── timeout-port/
│   │   └── timeout_port.go             # TimeoutProxy interface
│   ├── command-port/
│   │   └── command_port.go             # CommandPort interface  
│   └── jsonrpc/
│       └── entities/
│           └── jsonrpc_entities.go     # JSON-RPC message types
└── adapters/                           # Adapter implementations
    ├── default-timeout-proxy/
    │   └── default_timeout_proxy.go    # TimeoutProxy implementation
    └── default-command-adapter/
        └── default_command_adapter.go  # CommandPort implementation
```

### Adding New Adapters

To create a custom timeout implementation:

1. Implement the `TimeoutProxy` interface:
```go
type TimeoutProxy interface {
    Run() error
    HandleMessage(msg JSONRPCMessage) error
    Close() error
    GetTimeout() time.Duration
}
```

2. Implement the `TimeoutProxyFactory` interface:
```go
type TimeoutProxyFactory interface {
    NewTimeoutProxy(timeout time.Duration, commandPort CommandPort) (TimeoutProxy, error)
}
```

3. Update `main.go` to use your custom factory.

## Error Handling

The wrapper handles several error scenarios:

- **Subprocess startup failure**: Returns error immediately
- **Tool call timeout**: Returns JSON-RPC error response
- **Invalid JSON-RPC**: Logs error and continues processing
- **Subprocess termination**: Graceful cleanup on exit

## Performance Considerations

- **Memory**: Minimal overhead - only stores pending tool call channels
- **CPU**: Low overhead - mostly I/O bound operations
- **Concurrency**: Uses goroutines for non-blocking I/O operations
- **Cleanup**: Automatic subprocess termination on wrapper exit

## Contributing

We welcome contributions! Please follow these guidelines:

1. **Fork the repository** and create your feature branch from `main`
2. **Follow TDD practices** - write tests first
3. **Maintain the Ports and Adapters architecture** - keep interfaces clean
4. **Update documentation** for any API changes
5. **Ensure all tests pass** before submitting PRs
6. **Follow Go conventions** - use `gofmt`, `golint`, and `go vet`

For major changes, please open an issue first to discuss what you would like to change.

## Support

Need help or have questions? Here are your options:

- **Issues**: [Open a GitHub issue](https://github.com/chitacloud/timeout-mcp/issues) for bug reports or feature requests
- **Discussions**: [GitHub Discussions](https://github.com/chitacloud/timeout-mcp/discussions) for general questions and community support
- **Documentation**: Check our [troubleshooting section](#troubleshooting) for common issues

## Project Status

🟢 **Active Development** - This project is actively maintained and developed. New features are being added regularly, and bug reports are addressed promptly.

Current focus areas:
- Achieving 95%+ test coverage across all packages
- Performance optimizations for high-throughput scenarios
- Enhanced error handling and logging capabilities

## License

This project is licensed under the MIT License - see the [LICENSE.md](LICENSE.md) file for details.

```
MIT License

Copyright (c) 2025 Chita Cloud S.L.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Troubleshooting

### Common Issues

**Wrapper hangs on startup**
- Check that the target MCP server command is valid
- Verify all required arguments are provided
- Test the target server independently first

**Methods not timing out**
- Ensure you're testing with timeout-enabled methods: `tools/call`, `initialize`, or `tools/list`
- Other methods like notifications and resources are forwarded immediately without timeout
- Check timeout duration is appropriate for your use case

**Subprocess not responding**
- Verify the target MCP server accepts JSON-RPC on stdin
- Check server logs for initialization errors
- Test with a simple echo server first

### Debug Mode

For debugging, you can trace JSON-RPC messages:

```bash
# Enable verbose logging
GODEBUG=1 ./timeout-mcp --timeout 30s -- your-mcp-server
```

## Acknowledgments

This project was built using the Ports and Adapters (Hexagonal) architecture pattern, originally described by [Alistair Cockburn](http://alistair.cockburn.us/Hexagonal%2Barchitecture).

Special thanks to:
- The [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) specification creators
- The Go community for excellent tooling and libraries
- Contributors who helped improve test coverage and documentation

### Related Projects

- [MCP Specification](https://spec.modelcontextprotocol.io/) - Official MCP protocol documentation
- [MCP SDKs](https://github.com/modelcontextprotocol) - Official MCP implementations
