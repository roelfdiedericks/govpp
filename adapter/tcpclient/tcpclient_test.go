// Copyright (c) 2024 TCP adapter implementation for GoVPP.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tcpclient

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"go.fd.io/govpp/adapter"
)

// TestNewVppClient tests client creation with various addresses
func TestNewVppClient(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		expected string
	}{
		{
			name:     "empty address uses default",
			address:  "",
			expected: DefaultTCPAddress,
		},
		{
			name:     "custom address preserved",
			address:  "192.168.1.100:5002",
			expected: "192.168.1.100:5002",
		},
		{
			name:     "localhost address",
			address:  "localhost:9999",
			expected: "localhost:9999",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewVppClient(tt.address)
			if client.address != tt.expected {
				t.Errorf("expected address %s, got %s", tt.expected, client.address)
			}
		})
	}
}

// TestSetters tests the setter methods
func TestSetters(t *testing.T) {
	client := NewVppClient("localhost:5002")

	// Test SetClientName
	client.SetClientName("test-client")
	if client.clientName != "test-client" {
		t.Errorf("SetClientName failed: expected 'test-client', got '%s'", client.clientName)
	}

	// Test SetConnectTimeout
	timeout := 5 * time.Second
	client.SetConnectTimeout(timeout)
	if client.connectTimeout != timeout {
		t.Errorf("SetConnectTimeout failed: expected %v, got %v", timeout, client.connectTimeout)
	}

	// Test SetDisconnectTimeout
	disconnectTimeout := 200 * time.Millisecond
	client.SetDisconnectTimeout(disconnectTimeout)
	if client.disconnectTimeout != disconnectTimeout {
		t.Errorf("SetDisconnectTimeout failed: expected %v, got %v", disconnectTimeout, client.disconnectTimeout)
	}

	// Test SetKeepAlivePeriod
	keepAlive := 60 * time.Second
	client.SetKeepAlivePeriod(keepAlive)
	if client.keepAlivePeriod != keepAlive {
		t.Errorf("SetKeepAlivePeriod failed: expected %v, got %v", keepAlive, client.keepAlivePeriod)
	}
}

// TestWaitReady tests that WaitReady returns immediately for TCP
func TestWaitReady(t *testing.T) {
	client := NewVppClient("localhost:5002")
	
	start := time.Now()
	err := client.WaitReady()
	duration := time.Since(start)
	
	if err != nil {
		t.Errorf("WaitReady returned error: %v", err)
	}
	
	// Should return immediately (within 1ms)
	if duration > time.Millisecond {
		t.Errorf("WaitReady took too long: %v", duration)
	}
}

// TestGetMsgID tests message ID retrieval
func TestGetMsgID(t *testing.T) {
	client := NewVppClient("localhost:5002")
	
	// Set up a test message table
	client.setMsgTable(map[string]uint16{
		"test_msg_abc123": 100,
		"another_msg_def456": 200,
	}, 999)
	
	tests := []struct {
		name    string
		msgName string
		msgCrc  string
		wantID  uint16
		wantErr bool
	}{
		{
			name:    "existing message",
			msgName: "test_msg",
			msgCrc:  "abc123",
			wantID:  100,
			wantErr: false,
		},
		{
			name:    "another existing message",
			msgName: "another_msg",
			msgCrc:  "def456",
			wantID:  200,
			wantErr: false,
		},
		{
			name:    "non-existent message",
			msgName: "unknown_msg",
			msgCrc:  "xyz789",
			wantID:  0,
			wantErr: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := client.GetMsgID(tt.msgName, tt.msgCrc)
			
			if (err != nil) != tt.wantErr {
				t.Errorf("GetMsgID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			
			if id != tt.wantID {
				t.Errorf("GetMsgID() = %v, want %v", id, tt.wantID)
			}
			
			if err != nil {
				// Check it's the correct error type
				if _, ok := err.(*adapter.UnknownMsgError); !ok {
					t.Errorf("Expected UnknownMsgError, got %T", err)
				}
			}
		})
	}
}

// TestSendMsg tests message sending validation
func TestSendMsg(t *testing.T) {
	client := NewVppClient("localhost:5002")
	client.clientIndex = 42
	
	tests := []struct {
		name       string
		context    uint32
		data       []byte
		wantErrMsg string
	}{
		{
			name:       "valid message but not connected",
			context:    123,
			data:       make([]byte, 20), // Minimum 10 bytes required
			wantErrMsg: "not connected",
		},
		{
			name:       "too short message",
			context:    123,
			data:       make([]byte, 5), // Less than 10 bytes
			wantErrMsg: "invalid message data, length must be at least 10 bytes",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.SendMsg(tt.context, tt.data)
			
			if err == nil {
				t.Errorf("SendMsg() expected error, got nil")
				return
			}
			
			if err.Error() != tt.wantErrMsg {
				t.Errorf("SendMsg() error = %v, want %v", err.Error(), tt.wantErrMsg)
			}
		})
	}
}

// TestConnectionRefusal tests that connection to non-existent server fails appropriately
func TestConnectionRefusal(t *testing.T) {
	// Use a port that's unlikely to be in use
	client := NewVppClient("localhost:59999")
	client.SetConnectTimeout(100 * time.Millisecond) // Short timeout for test
	
	err := client.Connect()
	if err == nil {
		t.Error("Expected connection to fail, but it succeeded")
		client.Disconnect()
	}
	
	// Check that the error mentions connection failure
	if err != nil && !isConnectionError(err) {
		t.Errorf("Expected connection error, got: %v", err)
	}
}

// TestMockTCPServer tests basic TCP connection with a mock server
func TestMockTCPServer(t *testing.T) {
	// Start a mock TCP server
	listener, err := net.Listen("tcp", "localhost:0") // Use :0 to get random available port
	if err != nil {
		t.Fatalf("Failed to start mock server: %v", err)
	}
	defer listener.Close()
	
	serverAddr := listener.Addr().String()
	serverDone := make(chan bool)
	
	// Simple mock server that accepts connections and closes them
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		
		// Read the expected header (16 bytes)
		header := make([]byte, 16)
		n, _ := conn.Read(header)
		if n == 16 {
			// Echo back a simple response header
			conn.Write(header)
		}
		serverDone <- true
	}()
	
	// Test client connection
	client := NewVppClient(serverAddr)
	client.SetConnectTimeout(1 * time.Second)
	
	// The connect method itself should work
	err = client.connect()
	if err != nil {
		t.Errorf("Failed to connect to mock server: %v", err)
	}
	
	// Verify connection is established
	if client.conn == nil {
		t.Error("Connection not established")
	}
	
	// Clean up
	client.disconnect()
	
	select {
	case <-serverDone:
		// Server processed connection
	case <-time.After(1 * time.Second):
		t.Error("Server didn't receive connection")
	}
}

// TestGetMsgNameCrc tests the message name and CRC extraction
func TestGetMsgNameCrc(t *testing.T) {
	tests := []struct {
		name     string
		msg      []byte
		wantName string
		wantCrc  string
	}{
		{
			name: "valid message",
			msg: func() []byte {
				msg := make([]byte, 25)
				nameStr := "test_abc123"  // Format is name_crc, splits at first underscore
				binary.BigEndian.PutUint16(msg[3:5], uint16(len(nameStr))) // name length
				copy(msg[5:], []byte(nameStr))
				return msg
			}(),
			wantName: "test",
			wantCrc:  "abc123",
		},
		{
			name:     "empty message",
			msg:      []byte{},
			wantName: "",
			wantCrc:  "",
		},
		{
			name:     "too short message",
			msg:      []byte{0, 0, 0},
			wantName: "",
			wantCrc:  "",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, crc := getMsgNameCrc(tt.msg)
			if name != tt.wantName {
				t.Errorf("getMsgNameCrc() name = %v, want %v", name, tt.wantName)
			}
			if crc != tt.wantCrc {
				t.Errorf("getMsgNameCrc() crc = %v, want %v", crc, tt.wantCrc)
			}
		})
	}
}

// Helper function to check if error is connection-related
func isConnectionError(err error) bool {
	return err != nil && 
		(contains(err.Error(), "connection") ||
		 contains(err.Error(), "refused") ||
		 contains(err.Error(), "TCP"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && len(substr) == 0 || (len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
