// Copyright (c) 2017 Cisco and/or its affiliates.
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

package govpp

import (
	"strings"
	"time"

	"go.fd.io/govpp/adapter"
	"go.fd.io/govpp/adapter/shmclient"
	"go.fd.io/govpp/adapter/socketclient"
	"go.fd.io/govpp/adapter/tcpclient"
	"go.fd.io/govpp/core"
	"go.fd.io/govpp/internal/version"
)

// Connect connects to the VPP API using a new adapter instance created with NewVppAPIAdapter.
//
// This call blocks until VPP is connected, or an error occurs.
// Only one connection attempt will be performed.
//
// The target parameter accepts the following formats:
//   - "unix:///path/to/socket" - Unix domain socket connection
//   - "tcp://host:port" - TCP connection
//   - "shm://segment_name" - Shared memory connection (future)
//   - "/path/to/socket" - Unix socket (backward compatibility)
//   - "host:port" - TCP connection (if contains colon and no scheme)
func Connect(target string) (*core.Connection, error) {
	return core.Connect(NewVppAdapter(target))
}

// AsyncConnect asynchronously connects to the VPP API using a new adapter instance
// created with NewVppAPIAdapter.
//
// This call does not block until connection is established, it returns immediately.
// The caller is supposed to watch the returned ConnectionState channel for connection events.
// In case of disconnect, the library will asynchronously try to reconnect.
func AsyncConnect(target string, attempts int, interval time.Duration) (*core.Connection, chan core.ConnectionEvent, error) {
	return core.AsyncConnect(NewVppAdapter(target), attempts, interval)
}

// NewVppAdapter returns new instance of VPP adapter for connecting to VPP API.
// It automatically selects the appropriate adapter based on the target format.
var NewVppAdapter = func(target string) adapter.VppAPI {
	// Parse the target to determine connection type
	switch {
	case strings.HasPrefix(target, "tcp://"):
		// TCP connection with explicit scheme
		address := strings.TrimPrefix(target, "tcp://")
		return tcpclient.NewVppClient(address)
		
	case strings.HasPrefix(target, "unix://"):
		// Unix socket connection with explicit scheme
		path := strings.TrimPrefix(target, "unix://")
		return socketclient.NewVppClient(path)
		
	case strings.HasPrefix(target, "shm://"):
		// Shared memory connection
		shmName := strings.TrimPrefix(target, "shm://")
		return shmclient.NewVppClient(shmName)
		
	case strings.Contains(target, ":") && !strings.HasPrefix(target, "/"):
		// Assume TCP if it contains a colon and doesn't start with /
		// This handles cases like "localhost:5002" or "192.168.1.1:5002"
		return tcpclient.NewVppClient(target)
		
	default:
		// Default to Unix socket for backward compatibility
		// This handles cases like "/run/vpp/api.sock"
		return socketclient.NewVppClient(target)
	}
}

// Version returns version of GoVPP.
func Version() string {
	return version.Version()
}
