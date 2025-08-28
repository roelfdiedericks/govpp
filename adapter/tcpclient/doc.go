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

// Package tcpclient provides an adapter for VPP binary API over TCP.
//
// # TCP Connection
//
// The TCP adapter allows connecting to VPP over TCP sockets, which enables
// remote API access. This is useful for:
//   - Managing VPP instances on remote hosts
//   - Container/VM environments where Unix sockets aren't easily accessible
//   - Testing and development environments
//
// # Configuration
//
// To use TCP connection in VPP, configure the socksvr plugin:
//
//	socksvr {
//	    socket-name tcp:0.0.0.0:5002
//	}
//
// # Usage
//
// Create a TCP client and connect:
//
//	client := tcpclient.NewVppClient("192.168.1.100:5002")
//	conn, err := core.Connect(client)
//	if err != nil {
//	    // handle error
//	}
//	defer conn.Disconnect()
//
// Or use the connection string format:
//
//	conn, err := govpp.Connect("tcp://192.168.1.100:5002")
//
// # Security
//
// WARNING: The TCP adapter currently does not provide authentication or encryption.
// For production use, ensure the TCP connection is secured at the network level
// (e.g., VPN, SSH tunnel, or implement TLS support).
package tcpclient
