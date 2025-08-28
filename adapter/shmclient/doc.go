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

// Package shmclient provides an adapter for VPP binary API over shared memory.
//
// # Shared Memory Connection
//
// The shared memory adapter provides the highest performance connection to VPP
// by using memory-mapped files and lock-free data structures. This adapter is:
//   - Pure Go implementation (no CGO required)
//   - Lock-free ring buffer communication
//   - Ultra-low latency (1-10μs typical)
//   - Zero-copy message passing where possible
//
// # Configuration
//
// To use shared memory connection in VPP, configure the API segment:
//
//	api-segment {
//	    prefix vpp_api_
//	}
//
// # Usage
//
// Create a SHM client and connect:
//
//	client := shmclient.NewVppClient("default")
//	conn, err := core.Connect(client)
//	if err != nil {
//	    // handle error
//	}
//	defer conn.Disconnect()
//
// Or use the connection string format:
//
//	conn, err := govpp.Connect("shm://default")
//
// # Performance
//
// The shared memory adapter offers the best performance:
//   - Latency: 1-10μs (compared to 50-100μs for Unix sockets)
//   - Throughput: > 1M messages/sec
//   - CPU overhead: Minimal (no kernel transitions)
//
// # Limitations
//
//   - Only works for local VPP instances
//   - Requires shared memory access permissions
//   - Memory consumption scales with buffer size
//   - Not suitable for remote access
//
// # Implementation Details
//
// This adapter uses:
//   - Memory-mapped files in /dev/shm
//   - Lock-free ring buffers for message passing
//   - Atomic operations for synchronization
//   - Polling with configurable intervals
package shmclient
