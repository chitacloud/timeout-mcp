package defaulttimeoutproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/chitacloud/timeout-mcp/mocks"
	jsonrpcentities "github.com/chitacloud/timeout-mcp/ports/jsonrpc/entities"
)

func TestDefaultTimeoutProxyFactory_NewTimeoutProxy_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().Start().Return(nil)

	factory := &DefaultTimeoutProxyFactory{}
	timeout := 5 * time.Second

	proxy, err := factory.NewTimeoutProxy(timeout, false, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if proxy == nil {
		t.Fatal("Expected proxy to be created")
	}

	if proxy.GetTimeout() != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, proxy.GetTimeout())
	}
}

func TestDefaultTimeoutProxyFactory_NewTimeoutProxy_StartError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().Start().Return(io.ErrUnexpectedEOF)

	factory := &DefaultTimeoutProxyFactory{}
	timeout := 5 * time.Second

	proxy, err := factory.NewTimeoutProxy(timeout, false, mockCommandPort)
	if err == nil {
		t.Fatal("Expected error when Start() fails")
	}

	if proxy != nil {
		t.Error("Expected proxy to be nil on error")
	}

	if !strings.Contains(err.Error(), "failed to start command") {
		t.Errorf("Expected 'failed to start command' in error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_GetTimeout(t *testing.T) {
	timeout := 10 * time.Second
	proxy := &DefaultTimeoutProxy{
		timeout:      timeout,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	if proxy.GetTimeout() != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, proxy.GetTimeout())
	}
}

func TestDefaultTimeoutProxy_HandleMessage_NonToolCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	proxy := &DefaultTimeoutProxy{
		timeout:      5 * time.Second,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      "test-id",
	}

	err := proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify message was forwarded
	expectedData, _ := json.Marshal(msg)
	expectedData = append(expectedData, '\n')
	if !bytes.Equal(mockStdin.Buffer.Bytes(), expectedData) {
		t.Error("Message was not forwarded correctly")
	}
}

func TestDefaultTimeoutProxy_HandleMessage_ToolCall_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	proxy := &DefaultTimeoutProxy{
		timeout:      5 * time.Second,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      "tool-call-id",
	}

	// Start handleToolCall in goroutine since it blocks
	errChan := make(chan error, 1)
	go func() {
		errChan <- proxy.HandleMessage(msg)
	}()

	// Give time for message to be processed and added to pending calls
	time.Sleep(10 * time.Millisecond)

	// Simulate response from target
	response := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "tool-call-id",
		Result:  json.RawMessage(`{"success": true}`),
	}

	proxy.mu.RLock()
	responseChan, exists := proxy.pendingCalls["tool-call-id"]
	proxy.mu.RUnlock()

	if !exists {
		t.Fatal("Expected pending call to exist")
	}

	// Send response
	responseChan <- response

	// Wait for HandleMessage to complete
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("HandleMessage timed out")
	}

	// Verify message was forwarded
	expectedData, _ := json.Marshal(msg)
	expectedData = append(expectedData, '\n')
	if !bytes.Equal(mockStdin.Buffer.Bytes(), expectedData) {
		t.Error("Message was not forwarded correctly")
	}
}

func TestDefaultTimeoutProxy_HandleMessage_ToolCall_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	timeout := 50 * time.Millisecond
	proxy := &DefaultTimeoutProxy{
		timeout:      timeout,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      "tool-call-id",
	}

	err := proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify pending call was cleaned up after timeout
	time.Sleep(100 * time.Millisecond) // Wait for timeout to occur
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["tool-call-id"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after timeout")
	}
}

func TestDefaultTimeoutProxy_forwardMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "test",
		ID:      "test-id",
	}

	err := proxy.forwardMessage(msg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	expectedData, _ := json.Marshal(msg)
	expectedData = append(expectedData, '\n')
	if !bytes.Equal(mockStdin.Buffer.Bytes(), expectedData) {
		t.Error("Message was not forwarded correctly")
	}
}

func TestDefaultTimeoutProxy_forwardMessage_WriteError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &errorWriteCloser{}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "test",
		ID:      "test-id",
	}

	err := proxy.forwardMessage(msg)
	if err == nil {
		t.Fatal("Expected error when writing fails")
	}

	if !strings.Contains(err.Error(), "failed to write to target stdin") {
		t.Errorf("Expected 'failed to write to target stdin' in error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_sendToClient_Success(t *testing.T) {
	proxy := &DefaultTimeoutProxy{}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Result:  json.RawMessage(`{"success": true}`),
		ID:      "test-id",
	}

	// This test is hard to verify without capturing stdout
	// For now, just test that it doesn't error
	err := proxy.sendToClient(msg)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Close(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().Stop().Return(nil)

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
	}

	err := proxy.Close()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Close_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().Stop().Return(io.ErrUnexpectedEOF)

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
	}

	err := proxy.Close()
	if err == nil {
		t.Fatal("Expected error when Stop() fails")
	}

	if err != io.ErrUnexpectedEOF {
		t.Errorf("Expected ErrUnexpectedEOF, got %v", err)
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_NonToolCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Test data with non-tool call ID
	responseData := "{\"jsonrpc\":\"2.0\",\"id\":\"non-tool\",\"result\":{\"success\":true}}\n"
	mockStdout := &mockReadCloser{strings.NewReader(responseData)}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Create proxy with pending call
	responseChan := make(chan jsonrpcentities.JSONRPCMessage, 1)
	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: map[any]chan jsonrpcentities.JSONRPCMessage{"non-tool": responseChan},
		mu:           sync.RWMutex{},
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
	}

	// Start reader
	go proxy.readTargetResponses()

	// Wait for response
	select {
	case response := <-responseChan:
		if response.ID != "non-tool" {
			t.Errorf("Expected ID 'non-tool', got %v", response.ID)
		}
		if response.Result == nil {
			t.Error("Expected result to be present")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Response not received in time")
	}

	// Stop reader and wait for completion
	close(proxy.stopReader)

	// Wait for reader to complete with timeout
	select {
	case <-proxy.readerDone:
		// Success - reader completed
	case <-time.After(200 * time.Millisecond):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_PendingToolCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock reader with tool call response
	responseData := `{"jsonrpc":"2.0","id":"tool-call-123","result":{"data":"test"}}`
	mockStdout := &mockReadCloser{strings.NewReader(responseData)}

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Create response channel for pending tool call
	responseChan := make(chan jsonrpcentities.JSONRPCMessage, 1)
	pendingCalls := make(map[any]chan jsonrpcentities.JSONRPCMessage)
	pendingCalls["tool-call-123"] = responseChan

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: pendingCalls,
		mu:           sync.RWMutex{},
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Check if response was sent to channel
	select {
	case response := <-responseChan:
		if response.ID != "tool-call-123" {
			t.Errorf("Expected ID 'tool-call-123', got %v", response.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Expected response to be sent to channel")
	}

	// Properly stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(200 * time.Millisecond):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock reader with invalid JSON
	responseData := `{"jsonrpc":"2.0","id":"test" INVALID JSON}`
	mockStdout := &mockReadCloser{strings.NewReader(responseData)}

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give time for method to read and process
	time.Sleep(50 * time.Millisecond)

	// Properly stop the reader
	close(proxy.stopReader)

	// Wait for completion (should handle invalid JSON gracefully)
	select {
	case <-proxy.readerDone:
		// Success - invalid JSON should be logged but not fail
	case <-time.After(200 * time.Millisecond):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_handleToolCall_ErrorForwarding(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &errorWriteCloser{}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	proxy := &DefaultTimeoutProxy{
		timeout:      5 * time.Second,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      "tool-call-id",
	}

	err := proxy.handleTimeoutMethod(msg)
	if err == nil {
		t.Fatal("Expected error when forwarding fails")
	}

	// Verify pending call was cleaned up on error
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["tool-call-id"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after forwarding error")
	}
}

func TestDefaultTimeoutProxy_handleToolCall_ChannelTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)

	timeout := 10 * time.Millisecond // Very short timeout
	proxy := &DefaultTimeoutProxy{
		timeout:      timeout,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/call",
		ID:      "timeout-test",
	}

	err := proxy.handleTimeoutMethod(msg)
	if err != nil {
		t.Errorf("Expected no error from handleToolCall, got: %v", err)
	}

	// Verify pending call was cleaned up after timeout
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["timeout-test"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after timeout")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_ChannelBlocked(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock reader with tool call response
	responseData := `{"jsonrpc":"2.0","id":"blocked-channel","result":{"data":"test"}}`
	mockStdout := &mockReadCloser{strings.NewReader(responseData)}

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Create a non-buffered channel that will block
	responseChan := make(chan jsonrpcentities.JSONRPCMessage)
	pendingCalls := make(map[any]chan jsonrpcentities.JSONRPCMessage)
	pendingCalls["blocked-channel"] = responseChan

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: pendingCalls,
		mu:           sync.RWMutex{},
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
	}

	// Start readTargetResponses in goroutine since it blocks
	go proxy.readTargetResponses()

	// Give time for method to read and process
	time.Sleep(50 * time.Millisecond)

	// Signal to stop the reader and wait for completion
	close(proxy.stopReader)

	// Wait for completion - should handle blocked channel via default case
	select {
	case <-proxy.readerDone:
		// Success - blocked channel should use default case to send directly to client
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_sendToClient_JSONMarshalError(t *testing.T) {
	proxy := &DefaultTimeoutProxy{}

	// Create a message with invalid content that cannot be marshaled
	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Result:  json.RawMessage(`function() { /* invalid for JSON */ }`),
		ID:      make(chan int), // Invalid type for JSON marshaling
	}

	err := proxy.sendToClient(msg)
	if err == nil {
		t.Fatal("Expected error when JSON marshaling fails")
	}

	if !strings.Contains(err.Error(), "failed to marshal response") {
		t.Errorf("Expected 'failed to marshal response' in error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_forwardMessage_JSONMarshalError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	// Don't expect GetStdin to be called since marshaling will fail first

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
	}

	// Create a message with invalid content that cannot be marshaled
	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "test",
		ID:      make(chan int), // Invalid type for JSON marshaling
	}

	err := proxy.forwardMessage(msg)
	if err == nil {
		t.Fatal("Expected error when JSON marshaling fails")
	}

	if !strings.Contains(err.Error(), "failed to marshal message") {
		t.Errorf("Expected 'failed to marshal message' in error, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Run_ScannerError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommand := mocks.NewMockCommandPort(ctrl)
	proxy := &DefaultTimeoutProxy{
		timeout:      5 * time.Second,
		commandPort:  mockCommand,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes for stdin/stdout
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	mockCommand.EXPECT().GetStdin().Return(stdinWriter).AnyTimes()
	mockCommand.EXPECT().GetStdout().Return(stdoutReader).AnyTimes()
	mockCommand.EXPECT().IsRunning().Return(true).AnyTimes()

	// Close stdin immediately to trigger scanner error
	stdinWriter.Close()
	stdoutWriter.Close()

	// Run should handle the scanner error gracefully
	err := proxy.Run()
	if err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("Expected closed pipe error, got: %v", err)
	}

	stdinReader.Close()
	stdoutReader.Close()
}

func TestDefaultTimeoutProxy_Run_ProcessNotRunning(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommand := mocks.NewMockCommandPort(ctrl)
	proxy := &DefaultTimeoutProxy{
		timeout:      5 * time.Second,
		commandPort:  mockCommand,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	mockCommand.EXPECT().GetStdin().Return(stdinWriter).AnyTimes()
	mockCommand.EXPECT().GetStdout().Return(stdoutReader).AnyTimes()

	// Mock IsRunning to return false (process not running) - allow multiple calls
	mockCommand.EXPECT().IsRunning().Return(false).AnyTimes()

	// Close pipes to simulate process exit
	go func() {
		time.Sleep(10 * time.Millisecond)
		stdoutWriter.Close()
		stdinWriter.Close()
	}()

	// Run should exit when process is not running
	err := proxy.Run()
	if err != nil {
		t.Logf("Run exited with error (expected): %v", err)
	}

	stdinReader.Close()
	stdoutReader.Close()
}

func TestDefaultTimeoutProxy_readTargetResponses_ChannelBlock(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommand := mocks.NewMockCommandPort(ctrl)
	proxy := &DefaultTimeoutProxy{
		timeout:      100 * time.Millisecond,
		commandPort:  mockCommand,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create a test response channel that's blocked
	respChan := make(chan jsonrpcentities.JSONRPCMessage)
	proxy.mu.Lock()
	proxy.pendingCalls["test-id"] = respChan
	proxy.mu.Unlock()

	// Create pipes
	stdoutReader, stdoutWriter := io.Pipe()
	mockCommand.EXPECT().GetStdout().Return(stdoutReader).AnyTimes()

	// Send a response message
	go func() {
		time.Sleep(10 * time.Millisecond)
		response := `{"jsonrpc":"2.0","id":"test-id","result":{"success":true}}`
		stdoutWriter.Write([]byte(response + "\n"))
		stdoutWriter.Close()
	}()

	// Start readTargetResponses - it should handle the channel send
	go proxy.readTargetResponses()

	// Try to receive on the channel with timeout
	select {
	case msg := <-respChan:
		t.Logf("Received message: %v", msg)
	case <-time.After(200 * time.Millisecond):
		t.Log("Channel receive timed out (may be expected)")
	}

	stdoutReader.Close()
}

func TestDefaultTimeoutProxy_shouldApplyTimeout(t *testing.T) {
	proxy := &DefaultTimeoutProxy{}

	testCases := []struct {
		method   string
		expected bool
	}{
		{"tools/call", true},
		{"initialize", true},
		{"tools/list", true},
		{"ping", false},
		{"notifications/cancelled", false},
		{"resources/list", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.method, func(t *testing.T) {
			result := proxy.shouldApplyTimeout(tc.method)
			if result != tc.expected {
				t.Errorf("shouldApplyTimeout(%q) = %v, expected %v", tc.method, result, tc.expected)
			}
		})
	}
}

func TestDefaultTimeoutProxy_HandleMessage_Initialize(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	proxy := &DefaultTimeoutProxy{
		timeout:      50 * time.Millisecond,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      "init-id",
		Params:  map[string]any{"capabilities": map[string]any{}},
	}

	err := proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error from HandleMessage for initialize, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_HandleMessage_ToolsList(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	proxy := &DefaultTimeoutProxy{
		timeout:      50 * time.Millisecond,
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/list",
		ID:      "tools-list-id",
	}

	err := proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error from HandleMessage for tools/list, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Run_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}

	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes to simulate stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Replace os.Stdin temporarily
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// Write test JSON-RPC message to stdin
	jsonMsg := `{"jsonrpc":"2.0","method":"initialize","id":"test"}`
	go func() {
		w.Write([]byte(jsonMsg + "\n"))
		w.Close()
	}()

	// Test Run method
	err = proxy.Run()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Verify message was forwarded
	expectedData, _ := json.Marshal(jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "initialize",
		ID:      "test",
	})
	expectedData = append(expectedData, '\n')

	if !bytes.Equal(mockStdin.Buffer.Bytes(), expectedData) {
		t.Error("Message was not forwarded correctly")
	}
}

func TestDefaultTimeoutProxy_Run_EmptyLines(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdout := &mockReadCloser{strings.NewReader("")}

	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes to simulate stdin with empty lines
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Replace os.Stdin temporarily
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// Write empty lines and close
	go func() {
		w.Write([]byte("\n\n\n"))
		w.Close()
	}()

	// Test Run method - should handle empty lines gracefully
	err = proxy.Run()
	if err != nil {
		t.Errorf("Expected no error for empty lines, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Run_InvalidJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdout := &mockReadCloser{strings.NewReader("")}

	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes to simulate stdin with invalid JSON
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Replace os.Stdin temporarily
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// Write invalid JSON and close
	go func() {
		w.Write([]byte(`{"invalid": "json" SYNTAX ERROR}` + "\n"))
		w.Close()
	}()

	// Test Run method - should handle invalid JSON gracefully
	err = proxy.Run()
	if err != nil {
		t.Errorf("Expected no error for invalid JSON, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_Run_HandleMessageError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockStdin := &errorWriteCloser{}

	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Create pipes to simulate stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	// Replace os.Stdin temporarily
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// Write valid JSON that will cause HandleMessage to fail
	jsonMsg := `{"jsonrpc":"2.0","method":"initialize","id":"test"}`
	go func() {
		w.Write([]byte(jsonMsg + "\n"))
		w.Close()
	}()

	// Test Run method - should handle HandleMessage errors gracefully
	err = proxy.Run()
	if err != nil {
		t.Errorf("Expected no error even with HandleMessage failure, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_sendToClient_WriteFailure(t *testing.T) {
	// Test sendToClient when os.Stdout write fails
	proxy := &DefaultTimeoutProxy{}

	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "test",
		Result:  map[string]any{"success": true},
	}

	// Capture original stdout
	origStdout := os.Stdout

	// Create a pipe but close the write end to cause write errors
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Failed to create pipe: %v", err)
	}
	r.Close() // Close read end
	os.Stdout = w
	w.Close() // Close write end to cause write failure

	// Restore stdout
	defer func() { os.Stdout = origStdout }()

	// This should handle the write error gracefully
	proxy.sendToClient(msg)
	// No assertion needed - we're just testing it doesn't panic
}

func TestDefaultTimeoutProxy_readTargetResponses_ScanError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create a mock reader that will cause scanner errors
	mockStdout := &mockReadCloser{strings.NewReader("invalid\x00json\nwith\x00null\x00bytes")}

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give time for processing
	time.Sleep(50 * time.Millisecond)

	// Signal to stop the reader and wait for completion
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success - should handle scan errors gracefully
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

// Helper types and functions for testing

type mockWriteCloser struct {
	*bytes.Buffer
}

func (m *mockWriteCloser) Close() error {
	return nil
}

type errorWriteCloser struct{}

func (e *errorWriteCloser) Write(p []byte) (n int, err error) {
	return 0, io.ErrClosedPipe
}

func (e *errorWriteCloser) Close() error {
	return nil
}

func TestDefaultTimeoutProxy_AutoRestart_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)

	// Mock expectations for restart sequence
	mockCommandPort.EXPECT().Stop().Return(nil)
	mockCommandPort.EXPECT().Start().Return(nil)

	// Mock expectations for new reader goroutine after restart
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Mock expectation for proxy.Close() at the end
	mockCommandPort.EXPECT().Stop().Return(nil)

	// Create proxy with auto-restart enabled
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 100 * time.Millisecond
	proxy, err := factory.NewTimeoutProxy(timeout, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Get concrete type to access restartCommand method
	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Add a pending call to test cleanup during restart
	concreteProxy.mu.Lock()
	concreteProxy.pendingCalls[1] = make(chan jsonrpcentities.JSONRPCMessage, 1)
	concreteProxy.mu.Unlock()

	// Test restartCommand method directly
	concreteProxy.restartCommand()

	// Verify pending calls were cleared
	concreteProxy.mu.RLock()
	if len(concreteProxy.pendingCalls) != 0 {
		t.Errorf("Expected pending calls to be cleared, got %d", len(concreteProxy.pendingCalls))
	}
	concreteProxy.mu.RUnlock()
}

func TestDefaultTimeoutProxy_AutoRestart_StopError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)

	// Mock expectations for restart sequence with stop error
	mockCommandPort.EXPECT().Stop().Return(fmt.Errorf("stop failed"))
	mockCommandPort.EXPECT().Start().Return(nil)

	// Mock expectation for proxy.Close() at the end
	mockCommandPort.EXPECT().Stop().Return(nil)

	// Create proxy with auto-restart enabled
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 100 * time.Millisecond
	proxy, err := factory.NewTimeoutProxy(timeout, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Get concrete type to access restartCommand method
	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Test restartCommand method handles stop error gracefully
	concreteProxy.restartCommand()
}

func TestDefaultTimeoutProxy_AutoRestart_StartError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)

	// Mock expectations for restart sequence with start error
	mockCommandPort.EXPECT().Stop().Return(nil)
	mockCommandPort.EXPECT().Start().Return(fmt.Errorf("start failed"))

	// Mock expectation for proxy.Close() at the end
	mockCommandPort.EXPECT().Stop().Return(nil)

	// Create proxy with auto-restart enabled
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 100 * time.Millisecond
	proxy, err := factory.NewTimeoutProxy(timeout, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Get concrete type to access restartCommand method
	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Test restartCommand method handles start error gracefully
	concreteProxy.restartCommand()
}

func TestDefaultTimeoutProxy_AutoRestartEnabled_Creation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)
	// Mock expectation for proxy.Close() at the end
	mockCommandPort.EXPECT().Stop().Return(nil)

	// Create proxy with auto-restart enabled
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 100 * time.Millisecond
	proxy, err := factory.NewTimeoutProxy(timeout, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Get concrete type to check autoRestart field
	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Verify autoRestart field is set correctly
	if !concreteProxy.autoRestart {
		t.Errorf("Expected autoRestart to be true, got false")
	}
}

func TestDefaultTimeoutProxy_AutoRestartDisabled_Creation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)
	// Mock expectation for proxy.Close() at the end
	mockCommandPort.EXPECT().Stop().Return(nil)

	// Create proxy with auto-restart disabled
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 100 * time.Millisecond
	proxy, err := factory.NewTimeoutProxy(timeout, false, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Get concrete type to check autoRestart field
	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Verify autoRestart field is set correctly
	if concreteProxy.autoRestart {
		t.Errorf("Expected autoRestart to be false, got true")
	}
}

func TestDefaultTimeoutProxy_HandleMessage_AutoRestartTimeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create proper mocks for stdin/stdout
	mockStdin := &mockWriteCloser{Buffer: &bytes.Buffer{}}
	mockStdout := &mockReadCloser{strings.NewReader("")}

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Initial start
	mockCommandPort.EXPECT().Start().Return(nil)

	// Expectations for HandleMessage IO operations (tools/call will timeout)
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Auto-restart expectations when timeout occurs (async, so use AnyTimes)
	mockCommandPort.EXPECT().Stop().Return(nil).AnyTimes()
	mockCommandPort.EXPECT().Start().Return(nil).AnyTimes()

	// Create proxy with auto-restart and very short timeout
	factory := &DefaultTimeoutProxyFactory{}
	timeout := 10 * time.Millisecond // Very short timeout to ensure it triggers
	proxy, err := factory.NewTimeoutProxy(timeout, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}
	defer proxy.Close()

	// Capture stdout to verify the error message contains restart notice
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Create a tools/call message that will timeout
	toolsCall := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "test-timeout",
		Method:  "tools/call",
		Params:  map[string]any{"name": "test_tool"},
	}

	// Handle the message - should timeout and trigger auto-restart
	err = proxy.HandleMessage(toolsCall)
	if err != nil {
		t.Errorf("Expected no error from HandleMessage, got: %v", err)
	}

	// Restore stdout and read the captured output
	w.Close()
	os.Stdout = originalStdout

	output := make([]byte, 1024)
	n, _ := r.Read(output)
	response := string(output[:n])

	// Verify the response contains the auto-restart message
	if !strings.Contains(response, "restarting now") {
		t.Errorf("Expected response to contain 'restarting now', got: %s", response)
	}
}

func TestDefaultTimeoutProxy_HandleMessage_TimeoutMethodWithoutID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStdin := &mockWriteCloser{Buffer: &bytes.Buffer{}}
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Mock expectations for proxy creation
	mockCommandPort.EXPECT().Start().Return(nil)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)
	mockCommandPort.EXPECT().Stop().Return(nil) // For Close()

	factory := &DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(time.Second, false, mockCommandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test timeout method without ID (should forward directly)
	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "tools/call", // This triggers shouldApplyTimeout = true
		// No ID field - this should make it forward directly
		Params: map[string]any{"name": "test_tool"},
	}

	err = proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error for timeout method without ID, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_HandleMessage_NonTimeoutMethod(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStdin := &mockWriteCloser{Buffer: &bytes.Buffer{}}
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Mock expectations for proxy creation
	mockCommandPort.EXPECT().Start().Return(nil)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)
	mockCommandPort.EXPECT().Stop().Return(nil) // For Close()

	factory := &DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(time.Second, false, mockCommandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test non-timeout method (should forward directly)
	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "test-id",
		Method:  "ping", // This should not trigger timeout handling
		Params:  map[string]any{"data": "test"},
	}

	err = proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error for non-timeout method, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_HandleMessage_NotificationMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStdin := &mockWriteCloser{Buffer: &bytes.Buffer{}}
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Mock expectations for proxy creation
	mockCommandPort.EXPECT().Start().Return(nil)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin)
	mockCommandPort.EXPECT().Stop().Return(nil) // For Close()

	factory := &DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(time.Second, false, mockCommandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxy.Close()

	// Test notification message (no ID, should forward directly)
	msg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/cancelled",
		Params:  map[string]any{"reason": "user_cancelled"},
	}

	err = proxy.HandleMessage(msg)
	if err != nil {
		t.Errorf("Expected no error for notification message, got: %v", err)
	}
}

func TestDefaultTimeoutProxy_RestartCommand_ErrorPaths(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Mock expectations for initial creation
	mockCommandPort.EXPECT().Start().Return(nil)
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	factory := &DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(100*time.Millisecond, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer func() {
		mockCommandPort.EXPECT().Stop().Return(nil)
		proxy.Close()
	}()

	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Test restartCommand when Stop fails
	mockCommandPort.EXPECT().Stop().Return(fmt.Errorf("stop failed"))
	mockCommandPort.EXPECT().Start().Return(nil)

	concreteProxy.restartCommand()

	// Test restartCommand when Start fails
	mockCommandPort.EXPECT().Stop().Return(nil)
	mockCommandPort.EXPECT().Start().Return(fmt.Errorf("start failed"))

	concreteProxy.restartCommand()
}

func TestDefaultTimeoutProxy_RestartCommand_ChannelHandling(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	// Mock expectations for initial creation
	mockCommandPort.EXPECT().Start().Return(nil)
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	factory := &DefaultTimeoutProxyFactory{}
	proxy, err := factory.NewTimeoutProxy(100*time.Millisecond, true, mockCommandPort)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}
	defer func() {
		mockCommandPort.EXPECT().Stop().Return(nil)
		proxy.Close()
	}()

	concreteProxy := proxy.(*DefaultTimeoutProxy)

	// Test with nil channels (simulating tests that create proxy manually)
	concreteProxy.stopReader = nil
	concreteProxy.readerDone = nil

	// Test restartCommand with nil channels
	mockCommandPort.EXPECT().Stop().Return(nil)
	mockCommandPort.EXPECT().Start().Return(nil)

	concreteProxy.restartCommand()

	// Verify channels were recreated
	if concreteProxy.stopReader == nil {
		t.Error("Expected stopReader to be recreated")
	}
	if concreteProxy.readerDone == nil {
		t.Error("Expected readerDone to be recreated")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_NilStdout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
		stopReader:  make(chan struct{}),
		readerDone:  make(chan struct{}),
	}

	// Mock GetStdout to return nil initially, then a real reader
	mockCommandPort.EXPECT().GetStdout().Return(nil).Times(2)
	mockStdout := &mockReadCloser{strings.NewReader("")}
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give it time to hit the nil stdout case and retry
	time.Sleep(50 * time.Millisecond)

	// Stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_JSONParseError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create reader with invalid JSON
	invalidJSON := "invalid json line\n"
	mockStdout := &mockReadCloser{strings.NewReader(invalidJSON)}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort: mockCommandPort,
		stopReader:  make(chan struct{}),
		readerDone:  make(chan struct{}),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	// Stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_EmptyLines(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create reader with empty lines and valid JSON
	content := "\n\n   \n{\"jsonrpc\":\"2.0\",\"id\":\"test\",\"result\":{}}\n"
	mockStdout := &mockReadCloser{strings.NewReader(content)}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	// Stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_PendingCallChannelClosed(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create reader with tool call response
	responseData := "{\"jsonrpc\":\"2.0\",\"id\":\"tool-call-123\",\"result\":{\"data\":\"test\"}}\n"
	mockStdout := &mockReadCloser{strings.NewReader(responseData)}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	// Create buffered response channel for pending tool call, then fill it to block
	responseChan := make(chan jsonrpcentities.JSONRPCMessage, 1)
	responseChan <- jsonrpcentities.JSONRPCMessage{} // Fill the buffer to make it block on next send

	pendingCalls := make(map[any]chan jsonrpcentities.JSONRPCMessage)
	pendingCalls["tool-call-123"] = responseChan

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
		pendingCalls: pendingCalls,
		mu:           sync.RWMutex{},
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give it time to process and attempt to send to blocked channel
	time.Sleep(100 * time.Millisecond)

	// Stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_readTargetResponses_DirectForward(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create reader with notification message (no ID)
	notificationData := "{\"jsonrpc\":\"2.0\",\"method\":\"notification\",\"params\":{}}\n"
	mockStdout := &mockReadCloser{strings.NewReader(notificationData)}
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockCommandPort.EXPECT().GetStdout().Return(mockStdout).AnyTimes()

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		stopReader:   make(chan struct{}),
		readerDone:   make(chan struct{}),
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
	}

	// Start readTargetResponses in goroutine
	go proxy.readTargetResponses()

	// Give it time to process
	time.Sleep(50 * time.Millisecond)

	// Stop the reader
	close(proxy.stopReader)

	// Wait for completion
	select {
	case <-proxy.readerDone:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("readTargetResponses did not complete in time")
	}
}

func TestDefaultTimeoutProxy_sendAutoInitializeAndWait_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	// Store an initialization message
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "init-123",
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": true,
				},
			},
		},
	}

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		initMessage:  &initMsg,
	}

	// Create a goroutine to simulate the response
	go func() {
		time.Sleep(10 * time.Millisecond) // Small delay to simulate response

		// Simulate successful initialization response
		response := jsonrpcentities.JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      "init-123",
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{},
			},
		}

		proxy.mu.Lock()
		if responseChan, exists := proxy.pendingCalls["init-123"]; exists {
			select {
			case responseChan <- response:
				// Response sent successfully
			default:
				// Channel full or closed
			}
		}
		proxy.mu.Unlock()
	}()

	// Test the sendAutoInitializeAndWait function
	proxy.sendAutoInitializeAndWait()

	// Verify the pending call was cleaned up
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["init-123"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after successful response")
	}
}

func TestDefaultTimeoutProxy_sendAutoInitializeAndWait_NoStoredMessage(t *testing.T) {
	proxy := &DefaultTimeoutProxy{
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		initMessage:  nil, // No stored message
	}

	// Should return early and not panic
	proxy.sendAutoInitializeAndWait()
}

func TestDefaultTimeoutProxy_sendAutoInitializeAndWait_Timeout(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	// Store an initialization message
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "init-timeout",
		Method:  "initialize",
		Params:  map[string]any{},
	}

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		initMessage:  &initMsg,
	}

	// Don't simulate any response - let it timeout
	start := time.Now()
	proxy.sendAutoInitializeAndWait()
	duration := time.Since(start)

	// Should timeout after 5 seconds
	if duration < 4*time.Second || duration > 6*time.Second {
		t.Errorf("Expected timeout around 5 seconds, got %v", duration)
	}

	// Verify the pending call was cleaned up
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["init-timeout"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after timeout")
	}
}

func TestDefaultTimeoutProxy_sendAutoInitializeAndWait_ForwardError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Mock stdin that will return an error when written to
	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &errorWriteCloser{}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	// Store an initialization message
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "init-error",
		Method:  "initialize",
		Params:  map[string]any{},
	}

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		initMessage:  &initMsg,
	}

	// Should return quickly due to forward error
	start := time.Now()
	proxy.sendAutoInitializeAndWait()
	duration := time.Since(start)

	// Should return immediately (not wait 5 seconds)
	if duration > 1*time.Second {
		t.Errorf("Expected immediate return on forward error, got %v", duration)
	}

	// Verify the pending call was cleaned up after error
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["init-error"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after forward error")
	}
}

func TestDefaultTimeoutProxy_sendAutoInitializeAndWait_ErrorResponse(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockCommandPort := mocks.NewMockCommandPort(ctrl)
	mockStdin := &mockWriteCloser{&bytes.Buffer{}}
	mockCommandPort.EXPECT().GetStdin().Return(mockStdin).AnyTimes()

	// Store an initialization message
	initMsg := jsonrpcentities.JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      "init-error-resp",
		Method:  "initialize",
		Params:  map[string]any{},
	}

	proxy := &DefaultTimeoutProxy{
		commandPort:  mockCommandPort,
		pendingCalls: make(map[any]chan jsonrpcentities.JSONRPCMessage),
		mu:           sync.RWMutex{},
		initMessage:  &initMsg,
	}

	// Create a goroutine to simulate an error response
	go func() {
		time.Sleep(10 * time.Millisecond) // Small delay to simulate response

		// Simulate error initialization response
		response := jsonrpcentities.JSONRPCMessage{
			JSONRPC: "2.0",
			ID:      "init-error-resp",
			Error: &jsonrpcentities.JSONRPCError{
				Code:    -32602,
				Message: "Invalid params",
			},
		}

		proxy.mu.Lock()
		if responseChan, exists := proxy.pendingCalls["init-error-resp"]; exists {
			select {
			case responseChan <- response:
				// Response sent successfully
			default:
				// Channel full or closed
			}
		}
		proxy.mu.Unlock()
	}()

	// Test the sendAutoInitializeAndWait function
	proxy.sendAutoInitializeAndWait()

	// Verify the pending call was cleaned up
	proxy.mu.RLock()
	_, exists := proxy.pendingCalls["init-error-resp"]
	proxy.mu.RUnlock()

	if exists {
		t.Error("Expected pending call to be cleaned up after error response")
	}
}

type mockReadCloser struct {
	*strings.Reader
}

func (m *mockReadCloser) Close() error {
	return nil
}
