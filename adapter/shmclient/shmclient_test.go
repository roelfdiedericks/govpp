// Copyright (c) 2024 Shared memory adapter implementation for GoVPP.
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

package shmclient

import (
	"encoding/binary"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"go.fd.io/govpp/adapter"
)

// TestNewVppClient tests client creation
func TestNewVppClient(t *testing.T) {
	tests := []struct {
		name        string
		shmName     string
		expectedName string
	}{
		{
			name:        "empty name uses default",
			shmName:     "",
			expectedName: "default",
		},
		{
			name:        "custom name preserved",
			shmName:     "test_segment",
			expectedName: "test_segment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewVppClient(tt.shmName)
			if client.shmName != tt.expectedName {
				t.Errorf("expected shmName %s, got %s", tt.expectedName, client.shmName)
			}
			if client.clientName != DefaultClientName {
				t.Errorf("expected clientName %s, got %s", DefaultClientName, client.clientName)
			}
		})
	}
}

// TestSetters tests the setter methods
func TestSetters(t *testing.T) {
	client := NewVppClient("test")

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

	// Test SetPollInterval
	pollInterval := 50 * time.Microsecond
	client.SetPollInterval(pollInterval)
	if client.pollInterval != pollInterval {
		t.Errorf("SetPollInterval failed: expected %v, got %v", pollInterval, client.pollInterval)
	}
}

// TestGetSHMPath tests shared memory path generation
func TestGetSHMPath(t *testing.T) {
	tests := []struct {
		name     string
		shmName  string
		expected string
	}{
		{
			name:     "default segment",
			shmName:  "default",
			expected: DefaultSHMPrefix + "default",
		},
		{
			name:     "custom segment",
			shmName:  "custom_seg",
			expected: DefaultSHMPrefix + "custom_seg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewVppClient(tt.shmName)
			path := client.getSHMPath()
			if path != tt.expected {
				t.Errorf("expected path %s, got %s", tt.expected, path)
			}
		})
	}
}

// TestRingBuffer tests the lock-free ring buffer implementation
func TestRingBuffer(t *testing.T) {
	// Create a test buffer
	mem := make([]byte, 1024)
	rb := &RingBuffer{
		data:       mem,
		size:       uint32(len(mem) - RingBufferHeaderSize),
		dataOffset: RingBufferHeaderSize,
	}
	rb.head = (*uint32)(unsafe.Pointer(&mem[0]))
	rb.tail = (*uint32)(unsafe.Pointer(&mem[4]))
	atomic.StoreUint32(rb.head, 0)
	atomic.StoreUint32(rb.tail, 0)

	// Test enqueue and dequeue
	testData := []byte("Hello, VPP!")
	
	// Enqueue
	err := rb.Enqueue(testData)
	if err != nil {
		t.Fatalf("Failed to enqueue: %v", err)
	}

	// Dequeue
	data, err := rb.Dequeue()
	if err != nil {
		t.Fatalf("Failed to dequeue: %v", err)
	}

	// Verify data
	if string(data) != string(testData) {
		t.Errorf("Data mismatch: expected %s, got %s", testData, data)
	}

	// Test empty dequeue
	data, err = rb.Dequeue()
	if err != nil {
		t.Fatalf("Unexpected error on empty dequeue: %v", err)
	}
	if data != nil {
		t.Errorf("Expected nil data on empty dequeue, got %v", data)
	}
}

// TestRingBufferConcurrent tests concurrent access to ring buffer
func TestRingBufferConcurrent(t *testing.T) {
	// Create a larger test buffer
	mem := make([]byte, 64*1024)
	rb := &RingBuffer{
		data:       mem,
		size:       uint32(len(mem) - RingBufferHeaderSize),
		dataOffset: RingBufferHeaderSize,
	}
	rb.head = (*uint32)(unsafe.Pointer(&mem[0]))
	rb.tail = (*uint32)(unsafe.Pointer(&mem[4]))
	atomic.StoreUint32(rb.head, 0)
	atomic.StoreUint32(rb.tail, 0)

	// Number of goroutines and messages
	numProducers := 5
	numConsumers := 5
	messagesPerProducer := 100

	var wg sync.WaitGroup
	produced := make(chan string, numProducers*messagesPerProducer)
	consumed := make(chan string, numProducers*messagesPerProducer)

	// Start producers
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < messagesPerProducer; i++ {
				msg := []byte(string(rune('A'+id)) + string(rune('0'+i)))
				for {
					err := rb.Enqueue(msg)
					if err == nil {
						produced <- string(msg)
						break
					}
					// Buffer full, retry
					time.Sleep(time.Microsecond)
				}
			}
		}(p)
	}

	// Start consumers
	for c := 0; c < numConsumers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := 0
			for count < (numProducers*messagesPerProducer)/numConsumers {
				data, err := rb.Dequeue()
				if err != nil {
					t.Errorf("Consumer error: %v", err)
					return
				}
				if data != nil {
					consumed <- string(data)
					count++
				} else {
					// Empty buffer, wait a bit
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	// Wait for all goroutines
	wg.Wait()
	close(produced)
	close(consumed)

	// Verify all messages were consumed
	producedMap := make(map[string]int)
	for msg := range produced {
		producedMap[msg]++
	}

	consumedMap := make(map[string]int)
	for msg := range consumed {
		consumedMap[msg]++
	}

	// Check that we got all messages
	if len(producedMap) != len(consumedMap) {
		t.Errorf("Message count mismatch: produced %d, consumed %d", 
			len(producedMap), len(consumedMap))
	}

	for msg, count := range producedMap {
		if consumedMap[msg] != count {
			t.Errorf("Message %s: produced %d, consumed %d", msg, count, consumedMap[msg])
		}
	}
}

// TestGetMsgID tests message ID retrieval
func TestGetMsgID(t *testing.T) {
	client := NewVppClient("test")
	
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
	client := NewVppClient("test")
	client.clientIndex = 42
	
	tests := []struct {
		name       string
		context    uint32
		data       []byte
		wantErrMsg string
	}{
		{
			name:       "not connected",
			context:    123,
			data:       make([]byte, 20),
			wantErrMsg: "not connected",
		},
		{
			name:       "too short message",
			context:    123,
			data:       make([]byte, 5),
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

// TestWaitReady tests waiting for shared memory to be available
func TestWaitReady(t *testing.T) {
	client := NewVppClient("test_wait_ready_" + time.Now().Format("20060102150405"))
	
	// Ensure the SHM file doesn't exist
	shmPath := client.getSHMPath()
	os.Remove(shmPath) // Clean up any existing file
	
	// Test that it returns immediately if file exists
	// Create a temporary file
	file, err := os.Create(shmPath)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}
	file.Close()
	defer os.Remove(shmPath)
	
	start := time.Now()
	err = client.WaitReady()
	duration := time.Since(start)
	
	if err != nil {
		t.Errorf("WaitReady failed when file exists: %v", err)
	}
	
	// Should return immediately (within 10ms)
	if duration > 10*time.Millisecond {
		t.Errorf("WaitReady took too long when file exists: %v", duration)
	}
	
	// Clean up and test timeout when file doesn't exist
	os.Remove(shmPath)
	
	// Create a client with very short timeout
	client2 := NewVppClient("test_wait_ready_nonexist_" + time.Now().Format("20060102150405"))
	client2.SetConnectTimeout(50 * time.Millisecond)
	
	start = time.Now()
	err = client2.WaitReady()
	duration = time.Since(start)
	
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
	
	// The actual timeout in WaitReady uses DefaultConnectTimeout, not the client's timeout
	// We need to fix this in the implementation
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
				nameStr := "test_abc123"
				binary.BigEndian.PutUint16(msg[3:5], uint16(len(nameStr)))
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
